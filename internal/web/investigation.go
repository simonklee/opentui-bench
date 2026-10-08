package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"opentui-bench/internal/db"
	"opentui-bench/internal/profilediff"
	"opentui-bench/internal/record"
	"opentui-bench/internal/stats"
)

type investigationResponse struct {
	ID                        int64  `json:"id"`
	IdentityKey               string `json:"identity_key"`
	TriggerResultID           int64  `json:"trigger_result_id,omitempty"`
	Category                  string `json:"category"`
	Name                      string `json:"name"`
	BenchmarkKind             string `json:"benchmark_kind"`
	BaselineCommit            string `json:"baseline_commit"`
	TargetCommit              string `json:"target_commit"`
	StatisticalReferenceRunID int64  `json:"statistical_reference_run_id,omitempty"`
	Status                    string `json:"status"`
	CreatedAt                 string `json:"created_at"`
	UpdatedAt                 string `json:"updated_at"`
}

type attemptResponse struct {
	ID            int64  `json:"id"`
	AttemptKey    string `json:"attempt_key"`
	Role          string `json:"role"`
	CommitHash    string `json:"commit_hash"`
	Branch        string `json:"branch"`
	Samples       int    `json:"samples"`
	Profile       string `json:"profile"`
	JobID         int64  `json:"job_id,omitempty"`
	RunID         int64  `json:"run_id,omitempty"`
	BaselineRunID int64  `json:"baseline_run_id,omitempty"`
	TargetRunID   int64  `json:"target_run_id,omitempty"`
	Status        string `json:"status"`
	RecipeJSON    string `json:"recipe_json,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func investigationToResponse(inv *db.Investigation) investigationResponse {
	return investigationResponse{
		ID: inv.ID, IdentityKey: inv.IdentityKey, TriggerResultID: inv.TriggerResultID,
		Category: inv.Category, Name: inv.Name, BenchmarkKind: inv.BenchmarkKind,
		BaselineCommit: inv.BaselineCommit, TargetCommit: inv.TargetCommit,
		StatisticalReferenceRunID: inv.StatisticalReferenceRunID,
		Status:                    inv.Status, CreatedAt: inv.CreatedAt, UpdatedAt: inv.UpdatedAt,
	}
}

func attemptToResponse(a db.InvestigationAttempt) attemptResponse {
	return attemptResponse{
		ID: a.ID, AttemptKey: a.AttemptKey, Role: a.Role, CommitHash: a.CommitHash,
		Branch: a.Branch, Samples: a.Samples, Profile: a.Profile,
		JobID: a.JobID, RunID: a.RunID, BaselineRunID: a.BaselineRunID, TargetRunID: a.TargetRunID,
		Status: a.Status, RecipeJSON: a.RecipeJSON, Error: a.Error,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func newAttemptKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Server) handleInvestigationsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListInvestigations(w, r)
	case http.MethodPost:
		s.requireInvestigationAuth(s.handleCreateInvestigation)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) routeInvestigationsAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/investigations/")
	if path == "" {
		http.Error(w, "investigation id required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid investigation id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetInvestigation(w, r, id)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "evidence":
		s.handleInvestigationEvidence(w, r, id)
	case "candidates", "attempts", "record":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		authorize := s.requireInvestigationAuth
		if parts[1] == "record" {
			authorize = s.requireAuth
		}
		authorize(func(w http.ResponseWriter, r *http.Request) {
			switch parts[1] {
			case "candidates":
				s.handleCreateCandidate(w, r, id)
			case "attempts":
				s.handleCreatePairAttempt(w, r, id)
			case "record":
				s.handleRecordInvestigation(w, r, id)
			}
		})(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleListInvestigations(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err == nil && n > 0 {
			limit = n
		}
	}
	items, err := s.db.ListInvestigations(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := make([]investigationResponse, 0, len(items))
	for i := range items {
		response = append(response, investigationToResponse(&items[i]))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleGetInvestigation(w http.ResponseWriter, r *http.Request, id int64) {
	inv, err := s.db.GetInvestigation(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "investigation not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attempts, err := s.db.ListAttempts(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attemptResponses := make([]attemptResponse, 0, len(attempts))
	for _, attempt := range attempts {
		attemptResponses = append(attemptResponses, attemptToResponse(attempt))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"investigation": investigationToResponse(inv),
		"attempts":      attemptResponses,
		"actions":       investigationActions(attempts),
	})
}

func investigationActions(attempts []db.InvestigationAttempt) []string {
	actions := []string{"read_evidence"}
	if len(attempts) >= db.MaxAttemptsPerInvestigation {
		return actions
	}
	actions = append(actions, "run_pair")
	candidates := 0
	for _, attempt := range attempts {
		if attempt.Role == db.AttemptRoleCandidate {
			candidates++
		}
	}
	if candidates < db.MaxCandidateAttemptsPerInvestigation {
		actions = append(actions, "submit_candidate")
	}
	return actions
}

func (s *Server) handleCreateInvestigation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TriggerResultID           int64  `json:"trigger_result_id"`
		Category                  string `json:"category"`
		Name                      string `json:"name"`
		BenchmarkKind             string `json:"benchmark_kind"`
		BaselineCommit            string `json:"baseline_commit"`
		TargetCommit              string `json:"target_commit"`
		StatisticalReferenceRunID int64  `json:"statistical_reference_run_id"`
		AttemptKey                string `json:"attempt_key"`
		Branch                    string `json:"branch"`
		Samples                   int    `json:"samples"`
		Profile                   string `json:"profile"`
		RequestedBy               string `json:"requested_by"`
	}
	if !decodeInvestigationRequest(w, r, &req, 64<<10) {
		return
	}
	if req.TriggerResultID != 0 {
		result, err := s.db.GetResult(req.TriggerResultID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "trigger result not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Category == "" {
			req.Category = result.Category
		}
		if req.Name == "" {
			req.Name = result.Name
		}
		run, err := s.db.GetRun(result.RunID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.TargetCommit == "" {
			req.TargetCommit = run.CommitHashFull
			if req.TargetCommit == "" {
				req.TargetCommit = run.CommitHash
			}
		}
		if req.Branch == "" {
			req.Branch = run.Branch
		}
		if req.BenchmarkKind == "" {
			req.BenchmarkKind = run.BenchmarkKind
		}
		if req.BaselineCommit == "" && req.StatisticalReferenceRunID == 0 {
			window, err := s.db.GetComparableRunsWindow(run.ID, 8)
			if err == nil && len(window) > 1 {
				ref := window[1]
				req.StatisticalReferenceRunID = ref.ID
				req.BaselineCommit = ref.CommitHashFull
				if req.BaselineCommit == "" {
					req.BaselineCommit = ref.CommitHash
				}
			}
		}
	}
	if req.StatisticalReferenceRunID != 0 && req.BaselineCommit == "" {
		ref, err := s.db.GetRun(req.StatisticalReferenceRunID)
		if err != nil {
			http.Error(w, "statistical reference run not found", http.StatusBadRequest)
			return
		}
		req.BaselineCommit = ref.CommitHashFull
		if req.BaselineCommit == "" {
			req.BaselineCommit = ref.CommitHash
		}
	}
	if req.AttemptKey == "" {
		key, err := newAttemptKey()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.AttemptKey = key
	}
	inv, attempt, job, created, err := s.db.CreateInvestigationIfAbsent(db.InvestigationCreate{
		TriggerResultID:           req.TriggerResultID,
		Category:                  req.Category,
		Name:                      req.Name,
		BenchmarkKind:             req.BenchmarkKind,
		BaselineCommit:            req.BaselineCommit,
		TargetCommit:              req.TargetCommit,
		StatisticalReferenceRunID: req.StatisticalReferenceRunID,
		AttemptKey:                req.AttemptKey,
		Branch:                    req.Branch,
		Samples:                   req.Samples,
		Profile:                   req.Profile,
		RequestedBy:               req.RequestedBy,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too many") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	payload := map[string]any{
		"investigation": investigationToResponse(inv),
		"created":       created,
	}
	if attempt != nil {
		payload["attempt"] = attemptToResponse(*attempt)
	}
	if job != nil {
		payload["job"] = jobToResponse(job)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleCreateCandidate(w http.ResponseWriter, r *http.Request, investigationID int64) {
	inv, err := s.db.GetInvestigation(investigationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "investigation not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req struct {
		CommitHash  string `json:"commit_hash"`
		Branch      string `json:"branch"`
		AttemptKey  string `json:"attempt_key"`
		Samples     int    `json:"samples"`
		Profile     string `json:"profile"`
		RequestedBy string `json:"requested_by"`
		Notes       string `json:"notes"`
	}
	if !decodeInvestigationRequest(w, r, &req, 64<<10) {
		return
	}
	if req.AttemptKey == "" {
		key, err := newAttemptKey()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.AttemptKey = key
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	attempt, job, created, err := s.db.CreateCandidateAttempt(investigationID, db.InvestigationCreate{
		BenchmarkKind: inv.BenchmarkKind,
		Category:      inv.Category,
		Name:          inv.Name,
		TargetCommit:  req.CommitHash,
		AttemptKey:    req.AttemptKey,
		Branch:        req.Branch,
		Samples:       req.Samples,
		Profile:       req.Profile,
		RequestedBy:   req.RequestedBy,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too many") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"attempt": attemptToResponse(*attempt),
		"job":     jobToResponse(job),
		"created": created,
	})
}

func decodeInvestigationRequest(w http.ResponseWriter, r *http.Request, value any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) handleCreatePairAttempt(w http.ResponseWriter, r *http.Request, investigationID int64) {
	var req struct {
		AttemptKey  string `json:"attempt_key"`
		Samples     int    `json:"samples"`
		Profile     string `json:"profile"`
		RequestedBy string `json:"requested_by"`
	}
	if !decodeInvestigationRequest(w, r, &req, 64<<10) {
		return
	}
	if req.AttemptKey == "" {
		key, err := newAttemptKey()
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.AttemptKey = key
	}
	attempt, job, created, err := s.db.CreatePairAttempt(investigationID, db.InvestigationCreate{
		AttemptKey: req.AttemptKey, Samples: req.Samples, Profile: req.Profile, RequestedBy: req.RequestedBy,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "too many") {
			status = http.StatusConflict
		}
		writeJSONError(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"attempt": attemptToResponse(*attempt), "job": jobToResponse(job), "created": created,
	})
}

func (s *Server) handleRecordInvestigation(w http.ResponseWriter, r *http.Request, investigationID int64) {
	var recording record.InvestigationRecording
	if !decodeInvestigationRequest(w, r, &recording, record.MaxInvestigationRecordingBytes) {
		return
	}
	attempt, err := s.db.GetAttemptByKey(recording.AttemptKey)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeJSONError(w, err.Error(), status)
		return
	}
	if attempt.InvestigationID != investigationID {
		writeJSONError(w, "attempt does not belong to this investigation", http.StatusBadRequest)
		return
	}
	attempt, created, err := record.StoreInvestigation(s.db, recording, s.profileRetentionConfig())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"run_id": attempt.RunID, "baseline_run_id": attempt.BaselineRunID,
		"target_run_id": attempt.TargetRunID, "created": created,
	})
}

func (s *Server) handleInvestigationEvidence(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inv, err := s.db.GetInvestigation(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "investigation not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attempts, err := s.db.ListAttempts(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bundle, err := s.buildEvidenceBundle(inv, attempts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(bundle)
}

func (s *Server) buildEvidenceBundle(inv *db.Investigation, attempts []db.InvestigationAttempt) (map[string]any, error) {
	attemptResponses := make([]attemptResponse, 0, len(attempts))
	var comparison, latest *db.InvestigationAttempt
	for i := range attempts {
		attemptResponses = append(attemptResponses, attemptToResponse(attempts[i]))
		latest = &attempts[i]
		if attempts[i].Status == "completed" {
			comparison = &attempts[i]
		}
	}

	timing := map[string]any{
		"metric":            "avg_ns",
		"quantity":          "wall_time_per_operation",
		"lower_is_better":   true,
		"reproduced":        false,
		"comparison_status": "pending",
		"comparison_method": "paired_log_ratio_t_interval_95",
	}
	var baselineResult, targetResult *db.Result
	status := "open"
	if comparison == nil && latest != nil && (latest.Status == "failed" || latest.Status == "cancelled") {
		timing["comparison_status"] = latest.Status
		status = "unresolved"
	}
	if comparison != nil {
		timing["comparison_attempt_id"] = comparison.ID
		timing["comparison_attempt_role"] = comparison.Role
		runs, results, reason, err := s.comparisonMeasurements(inv, comparison)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			timing["comparison_status"] = "insufficient"
			timing["insufficient_reason"] = reason
			if comparison.Role == db.AttemptRoleCandidate {
				timing["candidate_comparison_status"] = "insufficient"
				timing["candidate_improved"] = false
			}
			status = "unresolved"
		} else {
			baselineResult, targetResult = results[0], results[1]
			lower, upper := pairedChangeInterval(baselineResult, targetResult)
			comparisonStatus := "inconclusive"
			status = "unresolved"
			reproduced := lower > 0 && runs[0].CommitHashFull != runs[1].CommitHashFull
			switch {
			case runs[0].CommitHashFull == runs[1].CommitHashFull:
				comparisonStatus = "unchanged_control"
			case reproduced:
				comparisonStatus, status = "reproduced", "reproduced"
			case upper <= 0:
				comparisonStatus = "not_reproduced"
			}
			timing["baseline_ns"], timing["target_ns"] = baselineResult.AvgNs, targetResult.AvgNs
			timing["baseline_run_id"], timing["target_run_id"] = runs[0].ID, runs[1].ID
			timing["change_percent"] = percentChange(baselineResult.AvgNs, targetResult.AvgNs)
			timing["paired_ratio_ci95_percent"] = []float64{lower, upper}
			timing["reproduced"], timing["comparison_status"] = reproduced, comparisonStatus
			if comparison.Role == db.AttemptRoleCandidate {
				cand := results[2]
				lower, upper := pairedChangeInterval(targetResult, cand)
				candidateStatus := "inconclusive"
				improved := upper < 0 && runs[2].CommitHashFull != runs[1].CommitHashFull
				switch {
				case runs[2].CommitHashFull == runs[1].CommitHashFull:
					candidateStatus = "unchanged_control"
				case improved:
					candidateStatus, status = "improved", "improved"
				case lower >= 0:
					candidateStatus = "not_improved"
				}
				timing["candidate_ns"], timing["candidate_run_id"] = cand.AvgNs, runs[2].ID
				timing["candidate_improved"], timing["candidate_comparison_status"] = improved, candidateStatus
				timing["candidate_change_percent"] = percentChange(targetResult.AvgNs, cand.AvgNs)
				timing["candidate_baseline_change_percent"] = percentChange(baselineResult.AvgNs, cand.AvgNs)
				timing["candidate_paired_ratio_ci95_percent"] = []float64{lower, upper}
			}
		}
	}
	if inv.Status != status {
		if err := s.db.UpdateInvestigationStatus(inv.ID, status); err != nil {
			return nil, err
		}
		inv.Status = status
	}

	profiles := s.profileEvidence(baselineResult, targetResult)
	if baselineResult == nil || targetResult == nil {
		if reason, ok := timing["insufficient_reason"].(string); ok && reason != "" {
			profiles["status"] = "insufficient"
			profiles["missing_reason"] = reason
		} else if comparison == nil && latest != nil && (latest.Status == "failed" || latest.Status == "cancelled") {
			profiles["status"] = latest.Status
		}
	}
	return map[string]any{
		"investigation": investigationToResponse(inv),
		"roles": map[string]string{
			"statistical_reference": "historical distribution that triggered the alert; not necessarily the predecessor commit",
			"baseline":              "explicit comparison revision measured in this investigation",
			"target":                "explicit flagged revision measured in this investigation",
			"candidate":             "proposed fix measured with fresh baseline and target controls in the same attempt",
		},
		"uncalibrated_regression_score": true,
		"attempts":                      attemptResponses,
		"timing":                        timing,
		"profiles":                      profiles,
		"source": map[string]any{
			"repository":            "https://github.com/anomalyco/opentui",
			"baseline_commit":       inv.BaselineCommit,
			"target_commit":         inv.TargetCommit,
			"disassembly_supported": false,
			"source_annotation":     "requires matching executable and debug information; folded stacks are insufficient",
		},
		"commands": map[string]any{
			"replay_pair": map[string]any{
				"method": "POST", "path": fmt.Sprintf("/api/investigations/%d/attempts", inv.ID),
				"body": map[string]any{"attempt_key": "<new-unique-key>", "samples": 3, "profile": "cpu"},
			},
			"submit_candidate": map[string]any{
				"method": "POST", "path": fmt.Sprintf("/api/investigations/%d/candidates", inv.ID),
				"body": map[string]any{"attempt_key": "<new-unique-key>", "commit_hash": "<candidate-commit>", "branch": "<candidate-branch>"},
			},
			"evidence": fmt.Sprintf("GET /api/investigations/%d/evidence", inv.ID),
		},
		"actions": investigationActions(attempts),
		"limits": map[string]int{
			"max_outstanding_investigation_jobs":       db.MaxOutstandingInvestigationJobs,
			"max_attempts_per_investigation":           db.MaxAttemptsPerInvestigation,
			"max_candidate_attempts_per_investigation": db.MaxCandidateAttemptsPerInvestigation,
			"max_samples_per_revision":                 db.MaxInvestigationSamples,
		},
	}, nil
}

func (s *Server) comparisonMeasurements(inv *db.Investigation, attempt *db.InvestigationAttempt) ([]*db.Run, []*db.Result, string, error) {
	ids := []int64{attempt.BaselineRunID, attempt.TargetRunID}
	roles := []string{db.AttemptRoleBaseline, db.AttemptRoleTarget}
	if attempt.Role == db.AttemptRoleCandidate {
		ids = append(ids, attempt.RunID)
		roles = append(roles, db.AttemptRoleCandidate)
	} else if attempt.Role != db.AttemptRolePair {
		return nil, nil, "unsupported comparison role", nil
	}
	if attempt.Samples < 2 {
		return nil, nil, "comparison requires at least two paired timing samples", nil
	}
	var recipes map[string]struct {
		CommitHash  string `json:"commit_hash"`
		ZigVersion  string `json:"zig_version"`
		ZigOptimize string `json:"zig_optimize"`
	}
	if err := json.Unmarshal([]byte(attempt.RecipeJSON), &recipes); err != nil || len(recipes) != len(roles) {
		return nil, nil, "per-revision build provenance is missing", nil
	}
	var runs []*db.Run
	var results []*db.Result
	for i, id := range ids {
		if id == 0 {
			return nil, nil, "fresh baseline and target controls from the same attempt are required", nil
		}
		run, err := s.db.GetRun(id)
		if err != nil {
			return nil, nil, "", err
		}
		zigVersion := db.CleanZigVersion(run.ZigVersion)
		if run.AttemptID != attempt.ID || run.Purpose != db.PurposeInvestigation || run.MachineID == "" || zigVersion == "" || run.CommitHashFull == "" {
			return nil, nil, "measurement provenance is incomplete or belongs to another attempt", nil
		}
		recipe := recipes[roles[i]]
		if recipe.CommitHash != run.CommitHashFull || db.CleanZigVersion(recipe.ZigVersion) != zigVersion || recipe.ZigOptimize != run.ZigOptimize {
			return nil, nil, "recorded build recipe contradicts the measurement identity", nil
		}
		if run.ZigOptimize != "ReleaseFast" && run.ZigOptimize != "ReleaseSafe" && run.ZigOptimize != "ReleaseSmall" {
			return nil, nil, "comparison requires optimized builds", nil
		}
		if len(runs) > 0 {
			base := runs[0]
			if run.MachineID != base.MachineID || run.ZigOptimize != base.ZigOptimize || zigVersion != db.CleanZigVersion(base.ZigVersion) ||
				run.BenchmarkKind != base.BenchmarkKind || run.BenchmarkSuite != base.BenchmarkSuite || run.ProtocolVersion != base.ProtocolVersion {
				return nil, nil, "paired measurements have different runner or build identities", nil
			}
		}
		result, err := s.resultForBenchmark(id, inv.Category, inv.Name)
		if err != nil {
			return nil, nil, "", err
		}
		if result.AvgNs <= 0 || result.TotalNs <= 0 || result.Iterations <= 0 {
			return nil, nil, "selected workload has no timing measurement", nil
		}
		if result.SampleCount != int64(attempt.Samples) || len(result.Samples) != attempt.Samples {
			return nil, nil, "paired timing samples are missing", nil
		}
		for i, sample := range result.Samples {
			if sample.SampleIndex != int64(i) || sample.AvgNs <= 0 {
				return nil, nil, "paired timing samples are missing or untimed", nil
			}
		}
		runs = append(runs, run)
		results = append(results, result)
	}
	return runs, results, "", nil
}

func pairedChangeInterval(baseline, target *db.Result) (float64, float64) {
	n := float64(len(baseline.Samples))
	var mean float64
	for i, sample := range baseline.Samples {
		mean += math.Log(float64(target.Samples[i].AvgNs)/float64(sample.AvgNs)) / n
	}
	var variance float64
	for i, sample := range baseline.Samples {
		delta := math.Log(float64(target.Samples[i].AvgNs)/float64(sample.AvgNs)) - mean
		variance += delta * delta / (n - 1)
	}
	margin := stats.TCriticalOneSided(len(baseline.Samples)-1, 0.025) * math.Sqrt(variance/n)
	return math.Expm1(mean-margin) * 100, math.Expm1(mean+margin) * 100
}

func percentChange(baseline, target int64) float64 {
	return (float64(target)/float64(baseline) - 1) * 100
}

func (s *Server) resultForBenchmark(runID int64, category, name string) (*db.Result, error) {
	results, err := s.db.GetResultsForRun(runID)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Category == category && results[i].Name == name {
			return &results[i], nil
		}
	}
	return nil, fmt.Errorf("benchmark %s/%s not found in run %d", category, name, runID)
}

func (s *Server) profileEvidence(baseline, target *db.Result) map[string]any {
	evidence := map[string]any{
		"quantity":      "sample_count",
		"unit":          "count",
		"capture_scope": "whole-process",
		"meaning":       "function sample counts and percentage shares; not measured CPU nanoseconds or wall time per operation",
	}
	if baseline == nil || target == nil {
		evidence["status"] = "pending"
		return evidence
	}
	baseArt, baseErr := s.db.GetArtifact(baseline.ID, cpuProfileKind)
	targArt, targErr := s.db.GetArtifact(target.ID, cpuProfileKind)
	if errors.Is(baseErr, sql.ErrNoRows) || errors.Is(targErr, sql.ErrNoRows) {
		evidence["status"] = "missing"
		evidence["missing_reason"] = "unknown"
		return evidence
	}
	if baseErr != nil {
		evidence["status"] = "error"
		evidence["error"] = baseErr.Error()
		return evidence
	}
	if targErr != nil {
		evidence["status"] = "error"
		evidence["error"] = targErr.Error()
		return evidence
	}
	diff, baseQ, targQ, err := profilediff.Diff(baseArt.DataBlob, targArt.DataBlob, 20)
	if err != nil {
		evidence["status"] = "error"
		evidence["error"] = err.Error()
		return evidence
	}
	status := "compared"
	if baseQ.Insufficient || targQ.Insufficient {
		status = "insufficient"
	}
	evidence["status"] = status
	evidence["baseline"] = baseQ
	evidence["target"] = targQ
	evidence["functions"] = diff
	evidence["baseline_result_id"] = baseline.ID
	evidence["target_result_id"] = target.ID
	evidence["baseline_profile"] = fmt.Sprintf("/api/runs/%d/results/%d/artifacts/cpu.pprof/download", baseline.RunID, baseline.ID)
	evidence["target_profile"] = fmt.Sprintf("/api/runs/%d/results/%d/artifacts/cpu.pprof/download", target.RunID, target.ID)
	return evidence
}
