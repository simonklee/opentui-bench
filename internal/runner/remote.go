package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/joblease"
	"opentui-bench/internal/record"
)

var ErrJobClaimLost = errors.New("remote job claim is no longer active")

const (
	remoteJobMaxAttempts     = 3
	remoteJobRetryDelay      = 100 * time.Millisecond
	remoteRunRecoveryTimeout = 10 * time.Second
)

type remoteHTTPError struct {
	method string
	path   string
	status int
	body   string
}

func (e *remoteHTTPError) Error() string {
	return fmt.Sprintf("%s %s: status %d: %s", e.method, e.path, e.status, e.body)
}

type remoteResponseError struct {
	method string
	path   string
	err    error
}

func (e *remoteResponseError) Error() string {
	return fmt.Sprintf("decode %s %s: %v", e.method, e.path, e.err)
}

func (e *remoteResponseError) Unwrap() error { return e.err }

// RemoteRecorder posts benchmark results to the Fly.io API.
type RemoteRecorder struct {
	BaseURL string       // e.g. "https://opentui-bench.fly.dev"
	APIKey  string       // Bearer token for auth
	Client  *http.Client // HTTP client (uses default if nil)
}

func (r *RemoteRecorder) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *RemoteRecorder) doRequest(req *http.Request) (*http.Response, error) {
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	return r.client().Do(req)
}

// createRunRequest is the JSON body for POST /api/runs.
type createRunRequest struct {
	CommitHash      string               `json:"commit_hash"`
	CommitHashFull  string               `json:"commit_hash_full"`
	CommitMessage   string               `json:"commit_message"`
	CommitDate      string               `json:"commit_date"`
	Branch          string               `json:"branch"`
	MachineID       string               `json:"machine_id"`
	Notes           string               `json:"notes"`
	ZigOptimize     string               `json:"zig_optimize"`
	BenchmarkKind   string               `json:"benchmark_kind"`
	BenchmarkSuite  string               `json:"benchmark_suite"`
	ProtocolVersion int64                `json:"protocol_version"`
	BunVersion      string               `json:"bun_version"`
	ZigVersion      string               `json:"zig_version"`
	ManifestHash    string               `json:"manifest_hash"`
	ManifestJSON    string               `json:"manifest_json"`
	Results         []createRunResultReq `json:"results"`
}

type createRunResultReq struct {
	Category             string               `json:"category"`
	Name                 string               `json:"name"`
	MinNs                int64                `json:"min_ns"`
	AvgNs                int64                `json:"avg_ns"`
	MaxNs                int64                `json:"max_ns"`
	StdDevNs             int64                `json:"std_dev_ns"`
	P50Ns                int64                `json:"p50_ns"`
	P95Ns                int64                `json:"p95_ns"`
	P99Ns                int64                `json:"p99_ns"`
	TotalNs              int64                `json:"total_ns"`
	Iterations           int64                `json:"iterations"`
	SampleCount          int64                `json:"sample_count"`
	SampleAvgVarianceNs2 *float64             `json:"sample_avg_variance_ns2,omitempty"`
	SampleDataVersion    int64                `json:"sample_data_version,omitempty"`
	SummaryVersion       int64                `json:"summary_version,omitempty"`
	Samples              []db.ResultSample    `json:"samples,omitempty"`
	MemStats             []record.MemStatJSON `json:"mem_stats,omitempty"`
}

type createRunResponse struct {
	ID             int64            `json:"id"`
	CommitHash     string           `json:"commit_hash"`
	CommitHashFull string           `json:"commit_hash_full"`
	RunDate        string           `json:"run_date"`
	ResultCount    int              `json:"result_count"`
	ResultIDs      map[string]int64 `json:"result_ids"`
	Results        []createdResult  `json:"results"`
}

type createdResult struct {
	ID       int64  `json:"id"`
	Category string `json:"category"`
	Name     string `json:"name"`
}

// RecordRun marshals the ParsedRun and POSTs it to /api/runs.
// Returns the run ID and a map of "category/name" -> result ID.
func (r *RemoteRecorder) RecordRun(ctx context.Context, parsed *record.ParsedRun) (int64, map[db.BenchmarkKey]int64, error) {
	if parsed.Meta.BenchmarkKind == string(BenchmarkJS) {
		if err := r.requireJavaScriptRunsCapability(ctx); err != nil {
			return 0, nil, err
		}
	}
	reqBody := createRunRequest{
		CommitHash:      parsed.Meta.CommitHash,
		CommitHashFull:  parsed.Meta.CommitHashFull,
		CommitMessage:   parsed.Meta.CommitMessage,
		CommitDate:      parsed.Meta.CommitDate,
		Branch:          parsed.Meta.Branch,
		MachineID:       parsed.Meta.MachineID,
		Notes:           parsed.Meta.Notes,
		ZigOptimize:     parsed.Meta.ZigOptimize,
		BenchmarkKind:   parsed.Meta.BenchmarkKind,
		BenchmarkSuite:  parsed.Meta.BenchmarkSuite,
		ProtocolVersion: parsed.Meta.ProtocolVersion,
		BunVersion:      parsed.Meta.BunVersion,
		ZigVersion:      parsed.Meta.ZigVersion,
		ManifestHash:    parsed.Meta.ManifestHash,
		ManifestJSON:    parsed.Meta.ManifestJSON,
	}

	for _, pr := range parsed.Results {
		reqBody.Results = append(reqBody.Results, createRunResultReq{
			Category:             pr.Category,
			Name:                 pr.Name,
			MinNs:                pr.MinNs,
			AvgNs:                pr.AvgNs,
			MaxNs:                pr.MaxNs,
			StdDevNs:             pr.StdDevNs,
			P50Ns:                pr.P50Ns,
			P95Ns:                pr.P95Ns,
			P99Ns:                pr.P99Ns,
			TotalNs:              pr.TotalNs,
			Iterations:           pr.Iterations,
			SampleCount:          pr.SampleCount,
			SampleAvgVarianceNs2: pr.SampleAvgVarianceNs2,
			SampleDataVersion:    pr.SampleDataVersion,
			SummaryVersion:       pr.SummaryVersion,
			Samples:              pr.Samples,
			MemStats:             pr.MemStats,
		})
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal request: %w", err)
	}

	requestCouldHaveReachedServer := ctx.Err() == nil
	result, err := r.postRun(ctx, body)
	if err != nil && requestCouldHaveReachedServer && isAmbiguousRunResponse(err) {
		recoveryParent := ctx
		if ctx.Err() != nil {
			recoveryParent = context.WithoutCancel(ctx)
		}
		recoveryCtx, cancel := context.WithTimeout(recoveryParent, remoteRunRecoveryTimeout)
		defer cancel()
		result, err = r.postRun(recoveryCtx, body)
		if err != nil {
			if recoveryCtx.Err() != nil && ctx.Err() != nil {
				return 0, nil, errors.Join(ctx.Err(), err)
			}
			return 0, nil, fmt.Errorf("recover POST /api/runs after ambiguous response: %w", err)
		}
	}
	if err != nil {
		return 0, nil, err
	}

	resultIDs := make(map[db.BenchmarkKey]int64, len(parsed.Results))
	for _, created := range result.Results {
		resultIDs[db.BenchmarkKey{Category: created.Category, Name: created.Name}] = created.ID
	}
	// Older deployed servers only return the legacy encoded map.
	if len(resultIDs) == 0 {
		legacyKeys := make(map[string]db.BenchmarkKey, len(parsed.Results))
		for _, parsedResult := range parsed.Results {
			key := db.BenchmarkKey{Category: parsedResult.Category, Name: parsedResult.Name}
			encoded := parsedResult.Category + "/" + parsedResult.Name
			if previous, exists := legacyKeys[encoded]; exists && previous != key {
				return 0, nil, fmt.Errorf("legacy server result key %q is ambiguous for %s/%s and %s/%s", encoded, previous.Category, previous.Name, key.Category, key.Name)
			}
			legacyKeys[encoded] = key
			if id, ok := result.ResultIDs[encoded]; ok {
				resultIDs[key] = id
			}
		}
	}
	return result.ID, resultIDs, nil
}

func (r *RemoteRecorder) postRun(ctx context.Context, body []byte) (createRunResponse, error) {
	const path = "/api/runs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return createRunResponse{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.doRequest(req)
	if err != nil {
		return createRunResponse{}, fmt.Errorf("POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return createRunResponse{}, &remoteHTTPError{method: http.MethodPost, path: path, status: resp.StatusCode, body: string(respBody)}
	}
	var result createRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return createRunResponse{}, &remoteResponseError{method: http.MethodPost, path: path, err: err}
	}
	if result.ID <= 0 {
		return createRunResponse{}, &remoteResponseError{method: http.MethodPost, path: path, err: errors.New("response has no run ID")}
	}
	return result, nil
}

func isAmbiguousRunResponse(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var responseErr *remoteResponseError
	if errors.As(err, &responseErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func (r *RemoteRecorder) requireJavaScriptRunsCapability(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+"/api/capabilities", nil)
	if err != nil {
		return fmt.Errorf("create capability request: %w", err)
	}
	resp, err := r.doRequest(req)
	if err != nil {
		return fmt.Errorf("GET /api/capabilities: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server does not support JavaScript run recording (capability status %d)", resp.StatusCode)
	}
	var capabilities struct {
		JavaScriptRuns int `json:"javascript_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil || capabilities.JavaScriptRuns != 1 {
		return fmt.Errorf("server does not support JavaScript run recording")
	}
	return nil
}

func (r *RemoteRecorder) requireJobLeaseCapability(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+"/api/capabilities", nil)
	if err != nil {
		return fmt.Errorf("create capability request: %w", err)
	}
	resp, err := r.doRequest(req)
	if err != nil {
		return fmt.Errorf("GET /api/capabilities: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server does not support job lease protocol %d (capability status %d)", joblease.Protocol, resp.StatusCode)
	}
	var capabilities struct {
		JobLeaseProtocol int `json:"job_lease_protocol"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil || capabilities.JobLeaseProtocol != joblease.Protocol {
		return fmt.Errorf("server does not support job lease protocol %d", joblease.Protocol)
	}
	return nil
}

// UploadArtifact sends a POST /api/runs/{runID}/results/{resultID}/artifacts
// with the raw artifact bytes.
func (r *RemoteRecorder) UploadArtifact(ctx context.Context, runID, resultID int64, artifact CollectedArtifact) error {
	u := fmt.Sprintf("%s/api/runs/%d/results/%d/artifacts", r.BaseURL, runID, resultID)

	params := url.Values{}
	params.Set("kind", artifact.Kind)
	if artifact.Metadata != "" {
		params.Set("metadata", artifact.Metadata)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"?"+params.Encode(), bytes.NewReader(artifact.Data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := r.doRequest(req)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload artifact: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// FinalizeArtifacts applies server-side retention after a run's complete
// profile set has been uploaded.
func (r *RemoteRecorder) FinalizeArtifacts(ctx context.Context, runID int64) error {
	u := fmt.Sprintf("%s/api/runs/%d/artifacts/finalize", r.BaseURL, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	finalizer := *r
	if finalizer.Client == nil {
		finalizer.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := finalizer.doRequest(req)
	if err != nil {
		return fmt.Errorf("finalize artifacts: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("finalize artifacts: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// HasCommit checks whether a commit has already been recorded.
func (r *RemoteRecorder) HasCommit(commitHashFull string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, r.BaseURL+"/api/has-commit/"+commitHashFull, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}

	resp, err := r.client().Do(req)
	if err != nil {
		return false, fmt.Errorf("GET /api/has-commit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("GET /api/has-commit: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Exists bool `json:"exists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}

	return result.Exists, nil
}

// LatestCommit returns the most recently recorded commit hash (full) for the
// given branch. If branch is empty, returns the latest across all branches.
// Returns empty string if no runs exist.
func (r *RemoteRecorder) LatestCommit(branch string) (string, error) {
	u := r.BaseURL + "/api/latest-commit"
	if branch != "" {
		u += "?branch=" + url.QueryEscape(branch)
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := r.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /api/latest-commit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GET /api/latest-commit: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		CommitHashFull *string `json:"commit_hash_full"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.CommitHashFull == nil {
		return "", nil
	}

	return *result.CommitHashFull, nil
}

// ClaimJob calls POST /api/jobs/claim to atomically claim the next pending job.
// An empty benchmark kind matches either kind. Returns nil if no job exists.
func (r *RemoteRecorder) ClaimJob(ctx context.Context, benchmarkKind string) (*JobClaimResponse, error) {
	if err := r.requireJobLeaseCapability(ctx); err != nil {
		return nil, err
	}
	claimToken, err := joblease.NewToken()
	if err != nil {
		return nil, fmt.Errorf("generate claim token: %w", err)
	}
	body, err := json.Marshal(struct {
		ClaimToken string `json:"claim_token"`
	}{ClaimToken: claimToken})
	if err != nil {
		return nil, fmt.Errorf("marshal claim request: %w", err)
	}

	u := r.BaseURL + "/api/jobs/claim"
	params := url.Values{}
	params.Set(joblease.QueryParameter, strconv.Itoa(joblease.Protocol))
	if benchmarkKind != "" {
		params.Set("benchmark_kind", benchmarkKind)
	}
	u += "?" + params.Encode()
	var lastErr error
	for attempt := 1; attempt <= remoteJobMaxAttempts; attempt++ {
		result, err := r.claimJobOnce(ctx, u, body)
		if err == nil {
			if result != nil {
				result.ClaimToken = claimToken
			}
			return result, nil
		}
		lastErr = err
		if !isTransientRemoteError(err) || attempt == remoteJobMaxAttempts {
			return nil, err
		}
		if err := waitForRemoteJobRetry(ctx, err); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (r *RemoteRecorder) claimJobOnce(ctx context.Context, target string, body []byte) (*JobClaimResponse, error) {
	const path = "/api/jobs/claim"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &remoteHTTPError{method: http.MethodPost, path: path, status: resp.StatusCode, body: string(respBody)}
	}
	var result JobClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &remoteResponseError{method: http.MethodPost, path: path, err: err}
	}
	if result.ID <= 0 || result.Status != "running" {
		return nil, &remoteResponseError{method: http.MethodPost, path: path, err: errors.New("response is not a running job")}
	}
	return &result, nil
}

// JobClaimResponse matches the job JSON response from the API.
type JobClaimResponse struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	Kind            string `json:"kind"`
	Branch          string `json:"branch"`
	CommitHash      string `json:"commit_hash"`
	RepoURL         string `json:"repo_url"`
	Samples         int    `json:"samples"`
	Profile         string `json:"profile"`
	Notes           string `json:"notes"`
	BenchmarkKind   string `json:"benchmark_kind"`
	BenchmarkSuite  string `json:"benchmark_suite"`
	ProtocolVersion int64  `json:"protocol_version"`
	ManifestHash    string `json:"manifest_hash"`
	ClaimToken      string `json:"-"`
}

// UpdateJob sends PATCH /api/jobs/{id} to update job status/commit/run_id.
func (r *RemoteRecorder) UpdateJob(ctx context.Context, jobID int64, claimToken string, update map[string]interface{}) error {
	if claimToken == "" {
		return fmt.Errorf("claim token is required")
	}
	payload := maps.Clone(update)
	payload["claim_token"] = claimToken
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/api/jobs/%d", r.BaseURL, jobID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.doRequest(req)
	if err != nil {
		return fmt.Errorf("PATCH /api/jobs/%d: %w", jobID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusConflict {
			return fmt.Errorf("%w: PATCH /api/jobs/%d: %s", ErrJobClaimLost, jobID, string(respBody))
		}
		return &remoteHTTPError{method: http.MethodPatch, path: fmt.Sprintf("/api/jobs/%d", jobID), status: resp.StatusCode, body: string(respBody)}
	}

	return nil
}

// CompleteJob, FailJob, and ReleaseJob make a terminal lease transition. A
// transient PATCH failure is ambiguous, so the current state is verified
// before the claim token is used again.
func (r *RemoteRecorder) CompleteJob(ctx context.Context, jobID int64, claimToken string, runID int64) error {
	return r.transitionJob(ctx, jobID, claimToken,
		map[string]interface{}{"status": "completed", "run_id": runID}, "completed", &runID)
}

func (r *RemoteRecorder) FailJob(ctx context.Context, jobID int64, claimToken, errMsg string) error {
	return r.transitionJob(ctx, jobID, claimToken,
		map[string]interface{}{"status": "failed", "error": errMsg}, "failed", nil)
}

func (r *RemoteRecorder) ReleaseJob(ctx context.Context, jobID int64, claimToken string) error {
	return r.transitionJob(ctx, jobID, claimToken,
		map[string]interface{}{"status": "pending"}, "pending", nil)
}

type remoteJobState struct {
	Status string `json:"status"`
	RunID  *int64 `json:"run_id"`
}

func (r *RemoteRecorder) transitionJob(ctx context.Context, jobID int64, claimToken string, update map[string]interface{}, wantStatus string, wantRunID *int64) error {
	var lastErr error
	for patchAttempt := 1; patchAttempt <= remoteJobMaxAttempts; patchAttempt++ {
		patchErr := r.UpdateJob(ctx, jobID, claimToken, update)
		lastErr = patchErr
		if patchErr == nil {
			return nil
		}
		if !isTransientRemoteError(patchErr) {
			return patchErr
		}

		for getAttempt := 1; ; getAttempt++ {
			state, getErr := r.getJob(ctx, jobID)
			if getErr == nil {
				if jobStateMatches(state, wantStatus, wantRunID) {
					return nil
				}
				if state.Status != "running" {
					return fmt.Errorf("%w: job %d is %s after ambiguous PATCH", ErrJobClaimLost, jobID, state.Status)
				}
				break
			}
			if !isTransientRemoteError(getErr) {
				return fmt.Errorf("verify job %d after ambiguous PATCH: %w", jobID, getErr)
			}
			if getAttempt == remoteJobMaxAttempts {
				return fmt.Errorf("verify job %d after ambiguous PATCH: %w", jobID, getErr)
			}
			if err := waitForRemoteJobRetry(ctx, getErr); err != nil {
				return err
			}
		}

		if patchAttempt == remoteJobMaxAttempts {
			return patchErr
		}
		if err := waitForRemoteJobRetry(ctx, patchErr); err != nil {
			return err
		}
	}
	return lastErr
}

func (r *RemoteRecorder) getJob(ctx context.Context, jobID int64) (remoteJobState, error) {
	path := fmt.Sprintf("/api/jobs/%d", jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+path, nil)
	if err != nil {
		return remoteJobState{}, fmt.Errorf("create request: %w", err)
	}
	resp, err := r.doRequest(req)
	if err != nil {
		return remoteJobState{}, fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return remoteJobState{}, &remoteHTTPError{method: http.MethodGet, path: path, status: resp.StatusCode, body: string(body)}
	}
	var state remoteJobState
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&state); err != nil {
		return remoteJobState{}, &remoteResponseError{method: http.MethodGet, path: path, err: err}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return remoteJobState{}, &remoteResponseError{method: http.MethodGet, path: path, err: errors.New("response must contain exactly one JSON value")}
	}
	switch state.Status {
	case "pending", "running", "completed", "failed", "cancelled":
	default:
		return remoteJobState{}, &remoteResponseError{method: http.MethodGet, path: path, err: fmt.Errorf("invalid job status %q", state.Status)}
	}
	return state, nil
}

func jobStateMatches(state remoteJobState, wantStatus string, wantRunID *int64) bool {
	if state.Status != wantStatus {
		return false
	}
	if wantRunID == nil {
		return state.RunID == nil
	}
	return state.RunID != nil && *state.RunID == *wantRunID
}

func isTransientRemoteError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *remoteHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.status >= http.StatusInternalServerError
	}
	var responseErr *remoteResponseError
	if errors.As(err, &responseErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func waitForRemoteJobRetry(ctx context.Context, lastErr error) error {
	timer := time.NewTimer(remoteJobRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("remote job transition did not complete after %v: %w", lastErr, ctx.Err())
	}
}
