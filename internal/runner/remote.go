package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

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
	CommitHash     string               `json:"commit_hash"`
	CommitHashFull string               `json:"commit_hash_full"`
	CommitMessage  string               `json:"commit_message"`
	CommitDate     string               `json:"commit_date"`
	Branch         string               `json:"branch"`
	MachineID      string               `json:"machine_id"`
	Notes          string               `json:"notes"`
	ZigOptimize    string               `json:"zig_optimize"`
	Results        []createRunResultReq `json:"results"`
}

type createRunResultReq struct {
	Category    string               `json:"category"`
	Name        string               `json:"name"`
	MinNs       int64                `json:"min_ns"`
	AvgNs       int64                `json:"avg_ns"`
	MaxNs       int64                `json:"max_ns"`
	StdDevNs    int64                `json:"std_dev_ns"`
	P50Ns       int64                `json:"p50_ns"`
	P95Ns       int64                `json:"p95_ns"`
	P99Ns       int64                `json:"p99_ns"`
	TotalNs     int64                `json:"total_ns"`
	Iterations  int64                `json:"iterations"`
	SampleCount int64                `json:"sample_count"`
	MemStats    []record.MemStatJSON `json:"mem_stats,omitempty"`
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
func (r *RemoteRecorder) RecordRun(parsed *record.ParsedRun) (int64, map[db.BenchmarkKey]int64, error) {
	reqBody := createRunRequest{
		CommitHash:     parsed.Meta.CommitHash,
		CommitHashFull: parsed.Meta.CommitHashFull,
		CommitMessage:  parsed.Meta.CommitMessage,
		CommitDate:     parsed.Meta.CommitDate,
		Branch:         parsed.Meta.Branch,
		MachineID:      parsed.Meta.MachineID,
		Notes:          parsed.Meta.Notes,
		ZigOptimize:    parsed.Meta.ZigOptimize,
	}

	for _, pr := range parsed.Results {
		reqBody.Results = append(reqBody.Results, createRunResultReq{
			Category:    pr.Category,
			Name:        pr.Name,
			MinNs:       pr.MinNs,
			AvgNs:       pr.AvgNs,
			MaxNs:       pr.MaxNs,
			StdDevNs:    pr.StdDevNs,
			P50Ns:       pr.P50Ns,
			P95Ns:       pr.P95Ns,
			P99Ns:       pr.P99Ns,
			TotalNs:     pr.TotalNs,
			Iterations:  pr.Iterations,
			SampleCount: pr.SampleCount,
			MemStats:    pr.MemStats,
		})
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, r.BaseURL+"/api/runs", bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.doRequest(req)
	if err != nil {
		return 0, nil, fmt.Errorf("POST /api/runs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, nil, fmt.Errorf("POST /api/runs: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result createRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, nil, fmt.Errorf("decode response: %w", err)
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

// UploadArtifact sends a POST /api/runs/{runID}/results/{resultID}/artifacts
// with the raw artifact bytes.
func (r *RemoteRecorder) UploadArtifact(runID, resultID int64, artifact CollectedArtifact) error {
	u := fmt.Sprintf("%s/api/runs/%d/results/%d/artifacts", r.BaseURL, runID, resultID)

	params := url.Values{}
	params.Set("kind", artifact.Kind)
	if artifact.Metadata != "" {
		params.Set("metadata", artifact.Metadata)
	}

	req, err := http.NewRequest(http.MethodPost, u+"?"+params.Encode(), bytes.NewReader(artifact.Data))
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
// Returns nil if no pending jobs exist.
func (r *RemoteRecorder) ClaimJob() (*JobClaimResponse, error) {
	req, err := http.NewRequest(http.MethodPost, r.BaseURL+"/api/jobs/claim", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := r.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("POST /api/jobs/claim: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /api/jobs/claim: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result JobClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// JobClaimResponse matches the job JSON response from the API.
type JobClaimResponse struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Kind       string `json:"kind"`
	Branch     string `json:"branch"`
	CommitHash string `json:"commit_hash"`
	RepoURL    string `json:"repo_url"`
	Samples    int    `json:"samples"`
	Profile    string `json:"profile"`
	Notes      string `json:"notes"`
}

// UpdateJob sends PATCH /api/jobs/{id} to update job status/commit/run_id.
func (r *RemoteRecorder) UpdateJob(jobID int64, update map[string]interface{}) error {
	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal update: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/jobs/%d", r.BaseURL, jobID), bytes.NewReader(body))
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
		return fmt.Errorf("PATCH /api/jobs/%d: status %d: %s", jobID, resp.StatusCode, string(respBody))
	}

	return nil
}
