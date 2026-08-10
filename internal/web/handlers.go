package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	"opentui-bench/internal/broadshift"
	"opentui-bench/internal/db"
	"opentui-bench/internal/joblease"
	"opentui-bench/internal/jsbench"
	"opentui-bench/internal/stats"
)

// handleRunsRoute dispatches /api/runs by method: GET lists runs, POST creates a run.
func (s *Server) handleRunsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRuns(w, r)
	case http.MethodPost:
		s.requireAuth(s.handleCreateRun)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	filter, err := runFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	branch := r.URL.Query().Get("branch")
	since := r.URL.Query().Get("since")

	runs, err := s.db.ListRunsFiltered(limit, branch, since, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type runResponse struct {
		runIdentityResponse
		ID            int64  `json:"id"`
		CommitHash    string `json:"commit_hash"`
		CommitMessage string `json:"commit_message"`
		Branch        string `json:"branch"`
		RunDate       string `json:"run_date"`
		Notes         string `json:"notes"`
		ResultCount   int    `json:"result_count"`
	}

	response := make([]runResponse, 0, len(runs))
	for _, run := range runs {
		count, err := s.db.CountResultsForRun(run.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response = append(response, runResponse{
			runIdentityResponse: identityResponse(&run),
			ID:                  run.ID,
			CommitHash:          run.CommitHash,
			CommitMessage:       run.CommitMessage,
			Branch:              run.Branch,
			RunDate:             run.RunDate,
			Notes:               run.Notes,
			ResultCount:         count,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	run, err := s.db.GetRun(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filter, err := explicitRunFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !filter.Matches(run) {
		http.Error(w, "run does not match requested benchmark identity", http.StatusNotFound)
		return
	}

	results, err := s.db.GetResultsForRun(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type memStatResponse struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	}

	type resultResponse struct {
		ID                   int64             `json:"id"`
		Category             string            `json:"category"`
		Name                 string            `json:"name"`
		MinNs                int64             `json:"min_ns"`
		AvgNs                int64             `json:"avg_ns"`
		MaxNs                int64             `json:"max_ns"`
		StdDevNs             int64             `json:"std_dev_ns"`
		P50Ns                int64             `json:"p50_ns"`
		P95Ns                int64             `json:"p95_ns"`
		P99Ns                int64             `json:"p99_ns"`
		Iterations           int64             `json:"iterations"`
		SampleCount          int64             `json:"sample_count"`
		SampleAvgVarianceNs2 *float64          `json:"sample_avg_variance_ns2"`
		SampleDataVersion    int64             `json:"sample_data_version"`
		SummaryVersion       int64             `json:"summary_version"`
		Samples              []db.ResultSample `json:"samples"`
		MemStats             []memStatResponse `json:"mem_stats,omitempty"`
	}

	type runDetailResponse struct {
		runIdentityResponse
		ID            int64            `json:"id"`
		CommitHash    string           `json:"commit_hash"`
		CommitMessage string           `json:"commit_message"`
		Branch        string           `json:"branch"`
		RunDate       string           `json:"run_date"`
		Notes         string           `json:"notes"`
		Results       []resultResponse `json:"results"`
	}

	var resultResponses []resultResponse
	for _, res := range results {
		rr := resultResponse{
			ID:                   res.ID,
			Category:             res.Category,
			Name:                 res.Name,
			MinNs:                res.MinNs,
			AvgNs:                res.AvgNs,
			MaxNs:                res.MaxNs,
			StdDevNs:             res.StdDevNs,
			P50Ns:                res.P50Ns,
			P95Ns:                res.P95Ns,
			P99Ns:                res.P99Ns,
			Iterations:           res.Iterations,
			SampleCount:          res.SampleCount,
			SampleAvgVarianceNs2: res.SampleAvgVarianceNs2,
			SampleDataVersion:    res.SampleDataVersion,
			SummaryVersion:       res.SummaryVersion,
			Samples:              res.Samples,
		}
		for _, ms := range res.MemStats {
			rr.MemStats = append(rr.MemStats, memStatResponse{
				Name:  ms.StatName,
				Bytes: ms.Bytes,
			})
		}
		resultResponses = append(resultResponses, rr)
	}

	response := runDetailResponse{
		runIdentityResponse: identityResponse(run),
		ID:                  run.ID,
		CommitHash:          run.CommitHash,
		CommitMessage:       run.CommitMessage,
		Branch:              run.Branch,
		RunDate:             run.RunDate,
		Notes:               run.Notes,
		Results:             resultResponses,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	idAStr := r.URL.Query().Get("id_a")
	idBStr := r.URL.Query().Get("id_b")
	commitA := r.URL.Query().Get("a")
	commitB := r.URL.Query().Get("b")
	filterFromRequest := runFilterFromRequest
	if idAStr != "" && idBStr != "" && r.URL.Query().Get("benchmark_kind") == jsbench.Kind {
		filterFromRequest = explicitRunFilterFromRequest
	}
	filter, err := filterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var resultsA, resultsB []db.Result
	var selectedA, selectedB *db.Run
	var runAHash, runBHash string

	if idAStr != "" && idBStr != "" {
		idA, errA := strconv.ParseInt(idAStr, 10, 64)
		idB, errB := strconv.ParseInt(idBStr, 10, 64)
		if errA != nil || errB != nil {
			http.Error(w, "invalid run IDs", http.StatusBadRequest)
			return
		}
		runA, err := s.db.GetRun(idA)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run A not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runB, err := s.db.GetRun(idB)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run B not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !filter.Matches(runA) || !filter.Matches(runB) {
			http.Error(w, "run does not match requested benchmark identity", http.StatusBadRequest)
			return
		}
		selectedA, selectedB = runA, runB
		runAHash, runBHash = runA.CommitHash, runB.CommitHash
		resultsA, err = s.db.GetResultsForRun(runA.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resultsB, err = s.db.GetResultsForRun(runB.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if commitA != "" && commitB != "" {
		runA, err := s.db.GetRunByCommitFiltered(commitA, filter)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run A not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runB, err := s.db.GetRunByCommitFiltered(commitB, filter)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run B not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		selectedA, selectedB = runA, runB
		runAHash, runBHash = runA.CommitHash, runB.CommitHash
		resultsA, err = s.db.GetResultsForRun(runA.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resultsB, err = s.db.GetResultsForRun(runB.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "provide either id_a & id_b or a & b parameters", http.StatusBadRequest)
		return
	}
	if !db.SameRunCohort(selectedA, selectedB) {
		http.Error(w, "runs have different benchmark identities", http.StatusBadRequest)
		return
	}

	// Use P50Ns (median) for comparison - more robust to outliers
	resultsBMap := make(map[db.BenchmarkKey]db.Result)
	for _, r := range resultsB {
		resultsBMap[db.BenchmarkKey{Category: r.Category, Name: r.Name}] = r
	}

	type comparison struct {
		Name             string            `json:"name"`
		Category         string            `json:"category"`
		BaselineNs       int64             `json:"baseline_ns"` // Median of baseline
		CurrentNs        int64             `json:"current_ns"`  // Median of current
		ChangePercent    float64           `json:"change_percent"`
		IsRegression     bool              `json:"is_regression"`
		BaselineResultID int64             `json:"baseline_result_id"`
		CurrentResultID  int64             `json:"current_result_id"`
		BaselineSamples  []db.ResultSample `json:"baseline_samples,omitempty"`
		CurrentSamples   []db.ResultSample `json:"current_samples,omitempty"`
	}

	var comparisons []comparison
	threshold := 10.0

	for _, rA := range resultsA {
		if resultB, ok := resultsBMap[db.BenchmarkKey{Category: rA.Category, Name: rA.Name}]; ok {
			var change float64
			if rA.P50Ns != 0 {
				change = float64(resultB.P50Ns-rA.P50Ns) / float64(rA.P50Ns) * 100
			}
			comparisons = append(comparisons, comparison{
				Name:             rA.Name,
				Category:         rA.Category,
				BaselineNs:       rA.P50Ns,
				CurrentNs:        resultB.P50Ns,
				ChangePercent:    change,
				IsRegression:     selectedB.BenchmarkKind == "zig" && change > threshold,
				BaselineResultID: rA.ID,
				CurrentResultID:  resultB.ID,
				BaselineSamples:  rA.Samples,
				CurrentSamples:   resultB.Samples,
			})
		}
	}

	response := struct {
		runIdentityResponse
		Baseline    string       `json:"baseline"`
		Current     string       `json:"current"`
		Comparisons []comparison `json:"comparisons"`
	}{
		runIdentityResponse: identityResponse(selectedB),
		Baseline:            runAHash,
		Current:             runBHash,
		Comparisons:         comparisons,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRuntimeCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	baselineID, baselineErr := strconv.ParseInt(r.URL.Query().Get("baseline_run_id"), 10, 64)
	comparedID, comparedErr := strconv.ParseInt(r.URL.Query().Get("compared_run_id"), 10, 64)
	if baselineErr != nil || comparedErr != nil || baselineID <= 0 || comparedID <= 0 {
		http.Error(w, "valid baseline_run_id and compared_run_id are required", http.StatusBadRequest)
		return
	}
	baselineRun, err := s.db.GetRun(baselineID)
	if err != nil {
		http.Error(w, "baseline run not found", http.StatusNotFound)
		return
	}
	comparedRun, err := s.db.GetRun(comparedID)
	if err != nil {
		http.Error(w, "compared run not found", http.StatusNotFound)
		return
	}
	baselineCommit := firstNonempty(baselineRun.CommitHashFull, baselineRun.CommitHash)
	comparedCommit := firstNonempty(comparedRun.CommitHashFull, comparedRun.CommitHash)
	if baselineCommit != comparedCommit || !db.CrossRuntimeCompatible(baselineRun, comparedRun) {
		http.Error(w, "runs are not cross-runtime compatible", http.StatusBadRequest)
		return
	}
	baselineResults, err := s.db.GetResultsForRun(baselineRun.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	comparedResults, err := s.db.GetResultsForRun(comparedRun.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type runtimeComparison struct {
		Category              string  `json:"category"`
		Name                  string  `json:"name"`
		BaselineResultID      int64   `json:"baseline_result_id"`
		ComparedResultID      int64   `json:"compared_result_id"`
		BaselineNS            int64   `json:"baseline_ns"`
		ComparedNS            int64   `json:"compared_ns"`
		DurationChangePercent float64 `json:"duration_change_percent"`
		SpeedRatio            float64 `json:"speed_ratio"`
	}
	comparedByKey := make(map[db.BenchmarkKey]db.Result, len(comparedResults))
	for _, result := range comparedResults {
		comparedByKey[db.BenchmarkKey{Category: result.Category, Name: result.Name}] = result
	}
	comparisons := make([]runtimeComparison, 0, len(baselineResults))
	for _, baseline := range baselineResults {
		compared, ok := comparedByKey[db.BenchmarkKey{Category: baseline.Category, Name: baseline.Name}]
		if !ok || baseline.P50Ns <= 0 || compared.P50Ns <= 0 {
			continue
		}
		comparisons = append(comparisons, runtimeComparison{
			Category: baseline.Category, Name: baseline.Name,
			BaselineResultID: baseline.ID, ComparedResultID: compared.ID,
			BaselineNS: baseline.P50Ns, ComparedNS: compared.P50Ns,
			DurationChangePercent: float64(compared.P50Ns-baseline.P50Ns) / float64(baseline.P50Ns) * 100,
			SpeedRatio:            float64(baseline.P50Ns) / float64(compared.P50Ns),
		})
	}
	type runtimeRun struct {
		runIdentityResponse
		ID             int64  `json:"id"`
		CommitHash     string `json:"commit_hash"`
		CommitHashFull string `json:"commit_hash_full,omitempty"`
	}
	response := struct {
		Metric      string              `json:"metric"`
		LowerBetter bool                `json:"lower_is_better"`
		Baseline    runtimeRun          `json:"baseline"`
		Compared    runtimeRun          `json:"compared"`
		Comparisons []runtimeComparison `json:"comparisons"`
	}{
		Metric: "p50_ns", LowerBetter: true,
		Baseline: runtimeRun{runIdentityResponse: identityResponse(baselineRun), ID: baselineRun.ID,
			CommitHash: baselineRun.CommitHash, CommitHashFull: baselineRun.CommitHashFull},
		Compared: runtimeRun{runIdentityResponse: identityResponse(comparedRun), ID: comparedRun.ID,
			CommitHash: comparedRun.CommitHash, CommitHashFull: comparedRun.CommitHashFull},
		Comparisons: comparisons,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) handleRuntimeTrend(w http.ResponseWriter, r *http.Request) {
	resultID, err := strconv.ParseInt(r.URL.Query().Get("result_id"), 10, 64)
	if err != nil || resultID <= 0 {
		http.Error(w, "valid result_id is required", http.StatusBadRequest)
		return
	}
	baselineRuntime := firstNonempty(r.URL.Query().Get("baseline_runtime"), jsbench.RuntimeBun)
	comparedRuntime := firstNonempty(r.URL.Query().Get("compared_runtime"), jsbench.RuntimeNode)
	if baselineRuntime == comparedRuntime || jsbench.RuntimeVersion(baselineRuntime) == "" || jsbench.RuntimeVersion(comparedRuntime) == "" {
		http.Error(w, "baseline_runtime and compared_runtime must be distinct canonical runtimes", http.StatusBadRequest)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > 500 {
			http.Error(w, "limit must be between 1 and 500", http.StatusBadRequest)
			return
		}
	}
	reference, err := s.db.GetResult(resultID)
	if err != nil {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	referenceRun, err := s.db.GetRun(reference.RunID)
	if err != nil || referenceRun.BenchmarkKind != jsbench.Kind {
		http.Error(w, "result is not a JavaScript benchmark", http.StatusBadRequest)
		return
	}
	rows, err := s.db.Query(`
		WITH baseline_runs AS (
			SELECT COALESCE(NULLIF(commit_hash_full, ''), commit_hash) AS commit_key, MAX(id) AS run_id
			FROM runs WHERE benchmark_kind = 'js' AND js_runtime = ? AND runtime_version = ?
			  AND machine_id = ? AND benchmark_suite = ? AND protocol_version = ?
			  AND zig_version = ? AND manifest_hash = ?
			GROUP BY COALESCE(NULLIF(commit_hash_full, ''), commit_hash)
		), compared_runs AS (
			SELECT COALESCE(NULLIF(commit_hash_full, ''), commit_hash) AS commit_key, MAX(id) AS run_id
			FROM runs WHERE benchmark_kind = 'js' AND js_runtime = ? AND runtime_version = ?
			  AND machine_id = ? AND benchmark_suite = ? AND protocol_version = ?
			  AND zig_version = ? AND manifest_hash = ?
			GROUP BY COALESCE(NULLIF(commit_hash_full, ''), commit_hash)
		), commits AS (
			SELECT commit_key FROM baseline_runs UNION SELECT commit_key FROM compared_runs
		)
		SELECT baseline_result.id, compared_result.id, commits.commit_key,
		       COALESCE(baseline_run.run_date, compared_run.run_date),
		       baseline_result.p50_ns, compared_result.p50_ns
		FROM commits
		LEFT JOIN baseline_runs ON baseline_runs.commit_key = commits.commit_key
		LEFT JOIN runs baseline_run ON baseline_run.id = baseline_runs.run_id
		LEFT JOIN results baseline_result ON baseline_result.run_id = baseline_run.id
		  AND baseline_result.category = ? AND baseline_result.name = ?
		LEFT JOIN compared_runs ON compared_runs.commit_key = commits.commit_key
		LEFT JOIN runs compared_run ON compared_run.id = compared_runs.run_id
		LEFT JOIN results compared_result ON compared_result.run_id = compared_run.id
		  AND compared_result.category = ? AND compared_result.name = ?
		WHERE baseline_result.id IS NOT NULL OR compared_result.id IS NOT NULL
		ORDER BY julianday(COALESCE(baseline_run.run_date, compared_run.run_date)) DESC,
		         COALESCE(baseline_run.id, compared_run.id) DESC LIMIT ?`,
		baselineRuntime, jsbench.RuntimeVersion(baselineRuntime), referenceRun.MachineID,
		referenceRun.BenchmarkSuite, referenceRun.ProtocolVersion, referenceRun.ZigVersion, referenceRun.ManifestHash,
		comparedRuntime, jsbench.RuntimeVersion(comparedRuntime), referenceRun.MachineID,
		referenceRun.BenchmarkSuite, referenceRun.ProtocolVersion, referenceRun.ZigVersion, referenceRun.ManifestHash,
		reference.Category, reference.Name, reference.Category, reference.Name, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()
	type pair struct {
		BaselineResultID *int64 `json:"baseline_result_id"`
		ComparedResultID *int64 `json:"compared_result_id"`
		CommitHash       string `json:"commit_hash"`
		RunDate          string `json:"run_date"`
		BaselineP50Ns    *int64 `json:"baseline_p50_ns"`
		ComparedP50Ns    *int64 `json:"compared_p50_ns"`
	}
	pairs := []pair{}
	for rows.Next() {
		var point pair
		var baselineResultID, comparedResultID, baselineP50NS, comparedP50NS sql.NullInt64
		if err := rows.Scan(&baselineResultID, &comparedResultID, &point.CommitHash, &point.RunDate,
			&baselineP50NS, &comparedP50NS); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if baselineResultID.Valid {
			point.BaselineResultID = &baselineResultID.Int64
		}
		if comparedResultID.Valid {
			point.ComparedResultID = &comparedResultID.Int64
		}
		if baselineP50NS.Valid {
			point.BaselineP50Ns = &baselineP50NS.Int64
		}
		if comparedP50NS.Valid {
			point.ComparedP50Ns = &comparedP50NS.Int64
		}
		pairs = append(pairs, point)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"baseline_runtime": baselineRuntime, "baseline_runtime_version": jsbench.RuntimeVersion(baselineRuntime),
		"compared_runtime": comparedRuntime, "compared_runtime_version": jsbench.RuntimeVersion(comparedRuntime),
		"pairs": pairs,
	})
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	resultID, err := strconv.ParseInt(r.URL.Query().Get("result_id"), 10, 64)
	if err != nil || resultID <= 0 {
		http.Error(w, "valid result_id parameter required", http.StatusBadRequest)
		return
	}
	filter, err := explicitRunFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	fetchLimit := limit
	minimumEvaluationDepth := defaultWindow + defaultBaselineOffset + 1
	if fetchLimit < minimumEvaluationDepth {
		fetchLimit = minimumEvaluationDepth
	}
	trends, err := s.db.GetTrend(resultID, fetchLimit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type trendPoint struct {
		runIdentityResponse
		RunID         int64  `json:"run_id"`
		ResultID      int64  `json:"result_id"`
		CommitHash    string `json:"commit_hash"`
		CommitMessage string `json:"commit_message,omitempty"`
		Branch        string `json:"branch"`
		RunDate       string `json:"run_date"`
		AvgNs         int64  `json:"avg_ns"`
		MedianNs      int64  `json:"median_ns"`
		MinNs         int64  `json:"min_ns"`
		MaxNs         int64  `json:"max_ns"`
		StdDevNs      int64  `json:"std_dev_ns"`
		SampleCount   int64  `json:"sample_count"`
		CiLowerNs     int64  `json:"ci_lower_ns"`
		CiUpperNs     int64  `json:"ci_upper_ns"`
		SemNs         int64  `json:"sem_ns"`
	}

	type trendResponse struct {
		Points            []trendPoint `json:"points"`
		AlgorithmVersion  string       `json:"algorithm_version"`
		Metric            string       `json:"metric"`
		Estimator         string       `json:"estimator"`
		CohortPolicy      string       `json:"cohort_policy"`
		FamilyDefinition  string       `json:"family_definition"`
		CalibrationStatus string       `json:"calibration_status"`
		CalibrationCaveat string       `json:"calibration_caveat"`
		FDRLevel          float64      `json:"fdr_level"`
		CurrentStatus     struct {
			RunID  int64  `json:"run_id"`
			Status string `json:"status"`
			Reason string `json:"reason,omitempty"`
		} `json:"current_status"`
	}

	var observations []stats.OrderedRunStat
	var target stats.OrderedRunStat
	var referenceRun *db.Run
	for _, t := range trends {
		runDate, err := time.Parse(time.RFC3339Nano, t.Run.RunDate)
		if err != nil {
			http.Error(w, "invalid run date: "+err.Error(), http.StatusInternalServerError)
			return
		}
		observation := stats.OrderedRunStat{RunDate: runDate, Stat: stats.RunStat{
			RunID: t.Run.ID, Avg: float64(t.Result.AvgNs),
		}}
		observations = append(observations, observation)
		if t.Result.ID == resultID {
			target = observation
			referenceRun = &t.Run
		}
	}
	if referenceRun == nil {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	if !filter.Matches(referenceRun) {
		http.Error(w, "result does not match requested benchmark identity", http.StatusBadRequest)
		return
	}

	var points []trendPoint
	for i, t := range trends {
		if i >= limit {
			break
		}
		ciLower, ciUpper, sem := stats.CI95(t.Result.AvgNs, t.Result.StdDevNs, t.Result.SampleCount)

		points = append(points, trendPoint{
			runIdentityResponse: identityResponse(&t.Run),
			RunID:               t.Run.ID,
			ResultID:            t.Result.ID,
			CommitHash:          t.Run.CommitHash,
			CommitMessage:       t.Run.CommitMessage,
			Branch:              t.Run.Branch,
			RunDate:             t.Run.RunDate,
			AvgNs:               t.Result.AvgNs,
			MedianNs:            t.Result.P50Ns,
			MinNs:               t.Result.MinNs,
			MaxNs:               t.Result.MaxNs,
			StdDevNs:            t.Result.StdDevNs,
			SampleCount:         t.Result.SampleCount,
			CiLowerNs:           ciLower,
			CiUpperNs:           ciUpper,
			SemNs:               sem,
		})
	}

	response := trendResponse{
		Points: points, AlgorithmVersion: regressionAlgorithmVersion, Metric: regressionMetric,
		Estimator: regressionEstimator, CohortPolicy: regressionCohortPolicy,
		FamilyDefinition: regressionFamilyDefinition, CalibrationStatus: regressionCalibrationStatus,
		CalibrationCaveat: regressionCalibrationCaveat, FDRLevel: defaultFDR,
	}
	response.CurrentStatus.RunID = target.Stat.RunID
	if referenceRun.BenchmarkKind == jsbench.Kind {
		response.CurrentStatus.Status = "disabled"
		response.CurrentStatus.Reason = "javascript_regressions_disabled"
	} else {
		evaluation := stats.EvaluateSnapshot(target, observations, stats.SnapshotConfig{
			Window: defaultWindow, MinPoints: defaultMinPoints, BaselineOffset: defaultBaselineOffset,
		})
		response.CurrentStatus.Status = evaluation.Result.Status
		if evaluation.Result.Status == "insufficient" {
			response.CurrentStatus.Reason = "insufficient_baseline_history"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleBenchmarks(w http.ResponseWriter, r *http.Request) {
	filter, err := runFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.Query(`SELECT DISTINCT r.category, r.name, COALESCE(ru.machine_id, ''), COALESCE(ru.zig_optimize, ''),
		ru.benchmark_kind, ru.benchmark_suite, ru.protocol_version, ru.bun_version, ru.zig_version, ru.manifest_hash, ru.manifest_json
		FROM results r JOIN runs ru ON ru.id = r.run_id ORDER BY r.category, r.name`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	type benchmarkResponse struct {
		db.BenchmarkKey
		runIdentityResponse
	}
	var keys []benchmarkResponse
	for rows.Next() {
		var key db.BenchmarkKey
		var run db.Run
		if err := rows.Scan(&key.Category, &key.Name, &run.MachineID, &run.ZigOptimize,
			&run.BenchmarkKind, &run.BenchmarkSuite, &run.ProtocolVersion, &run.BunVersion, &run.ZigVersion, &run.ManifestHash, &run.ManifestJSON); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if filter.Matches(&run) {
			keys = append(keys, benchmarkResponse{BenchmarkKey: key, runIdentityResponse: identityResponse(&run)})
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(keys); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/categories")

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	rows, err := s.db.Query(`SELECT DISTINCT category FROM results WHERE run_id = ? ORDER BY category`, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	var categories []string
	for rows.Next() {
		var cat string
		if err := rows.Scan(&cat); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		categories = append(categories, cat)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(categories); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

const (
	flamegraphSVGKind = "cpu.flamegraph.svg"
	callgraphSVGKind  = "cpu.callgraph.svg"
	cpuProfileKind    = "cpu.pprof"
	maxProfileSize    = 50 << 20
	maxFlamegraphSize = 20 << 20
	flamegraphTimeout = 30 * time.Second
)

func (s *Server) acquireFlamegraphSlot(ctx context.Context) error {
	if s.flamegraphSem == nil {
		return nil
	}
	select {
	case s.flamegraphSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) releaseFlamegraphSlot() {
	if s.flamegraphSem == nil {
		return
	}
	select {
	case <-s.flamegraphSem:
	default:
	}
}

func generateFlamegraphSVG(ctx context.Context, foldedStacks string, title string) ([]byte, error) {
	if _, err := exec.LookPath("inferno-flamegraph"); err != nil {
		return nil, fmt.Errorf("inferno-flamegraph not available: %w", err)
	}

	args := []string{}
	if title != "" {
		args = append(args, "--title", title)
	}

	cmd := exec.CommandContext(ctx, "inferno-flamegraph", args...)
	cmd.Stdin = strings.NewReader(foldedStacks)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return nil, fmt.Errorf("inferno-flamegraph: %w", err)
		}
		return nil, fmt.Errorf("inferno-flamegraph: %w (%s)", err, trimmed)
	}
	return output, nil
}

func generateCallgraphSVG(ctx context.Context, profileData []byte) ([]byte, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("go tool pprof not available: %w", err)
	}
	if _, err := exec.LookPath("dot"); err != nil {
		return nil, fmt.Errorf("graphviz dot not available: %w", err)
	}

	tmp, err := os.CreateTemp("", "opentui-pprof-*.pb.gz")
	if err != nil {
		return nil, fmt.Errorf("create temp profile: %w", err)
	}
	if _, err := tmp.Write(profileData); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("write temp profile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("close temp profile: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	cmd := exec.CommandContext(ctx, "go", "tool", "pprof", "-svg", tmp.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return nil, fmt.Errorf("go tool pprof: %w", err)
		}
		return nil, fmt.Errorf("go tool pprof: %w (%s)", err, trimmed)
	}
	return output, nil
}

func foldedStacksFromProfile(profileData []byte) (string, error) {
	prof, err := profile.ParseData(profileData)
	if err != nil {
		return "", fmt.Errorf("parse pprof: %w", err)
	}
	if len(prof.Sample) == 0 {
		return "", fmt.Errorf("pprof contains no samples")
	}

	sampleIndex := sampleIndexForProfile(prof)
	stacks := make(map[string]int64, len(prof.Sample))

	for _, sample := range prof.Sample {
		if sampleIndex >= len(sample.Value) {
			continue
		}
		value := sample.Value[sampleIndex]
		if value <= 0 {
			continue
		}

		frames := make([]string, 0, len(sample.Location))
		for i := len(sample.Location) - 1; i >= 0; i-- {
			loc := sample.Location[i]
			if len(loc.Line) == 0 {
				if loc.Address != 0 {
					frames = append(frames, fmt.Sprintf("0x%x", loc.Address))
				}
				continue
			}
			for _, line := range loc.Line {
				name := functionLabel(line.Function)
				if name == "" {
					continue
				}
				frames = append(frames, sanitizeFlamegraphFrame(name))
			}
		}

		if len(frames) == 0 {
			continue
		}
		stacks[strings.Join(frames, ";")] += value
	}

	if len(stacks) == 0 {
		return "", fmt.Errorf("pprof contained no usable samples")
	}

	keys := make([]string, 0, len(stacks))
	for key := range stacks {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s %d\n", key, stacks[key])
	}
	return b.String(), nil
}

func sampleIndexForProfile(prof *profile.Profile) int {
	if len(prof.SampleType) == 0 {
		return 0
	}
	preferred := []string{"samples", "cpu", "cpu_time", "time"}
	for _, want := range preferred {
		for i, sampleType := range prof.SampleType {
			if sampleType.Type == want {
				return i
			}
		}
	}
	return 0
}

func functionLabel(fn *profile.Function) string {
	if fn == nil {
		return ""
	}
	if fn.Name != "" {
		return fn.Name
	}
	return fn.SystemName
}

func sanitizeFlamegraphFrame(name string) string {
	name = strings.ReplaceAll(name, ";", ":")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	return strings.TrimSpace(name)
}

func (s *Server) handleFlamegraphList(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/flamegraphs")

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	profiled, err := s.db.ListFlamegraphResults(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type flamegraphItem struct {
		ResultID int64  `json:"result_id"`
		Name     string `json:"name"`
		Category string `json:"category"`
	}

	response := make([]flamegraphItem, 0, len(profiled))
	for _, row := range profiled {
		response = append(response, flamegraphItem{
			ResultID: row.ResultID,
			Name:     row.Name,
			Category: row.Category,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleFlamegraphSVG(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(path, "/results/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	suffix := strings.TrimSuffix(parts[1], "/flamegraph")
	if suffix == parts[1] {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	resultID, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil {
		http.Error(w, "invalid result id", http.StatusBadRequest)
		return
	}

	result, err := s.db.GetResult(resultID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if result.RunID != runID {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}

	cacheKey := fmt.Sprintf("result:%d", result.ID)
	legacyCacheKey := fmt.Sprintf("%d", result.ID)
	if s.svgCache != nil {
		if svg, ok := s.svgCache.Get(runID, cacheKey); ok {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write(svg)
			return
		}
		if legacyCacheKey != cacheKey {
			if svg, ok := s.svgCache.Get(runID, legacyCacheKey); ok {
				_ = s.svgCache.Put(runID, cacheKey, svg)
				w.Header().Set("Content-Type", "image/svg+xml")
				_, _ = w.Write(svg)
				return
			}
		}
	}

	cached, err := s.db.GetArtifact(result.ID, flamegraphSVGKind)
	if err == nil {
		if s.svgCache != nil {
			_ = s.svgCache.Put(runID, cacheKey, cached.DataBlob)
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(cached.DataBlob)
		return
	}

	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), flamegraphTimeout)
	defer cancel()

	if err := s.acquireFlamegraphSlot(ctx); err != nil {
		http.Error(w, "flamegraph generation busy", http.StatusServiceUnavailable)
		return
	}
	defer s.releaseFlamegraphSlot()

	var sameNameCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id = ? AND name = ?`, runID, result.Name).Scan(&sameNameCount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sameNameCount == 1 {
		fg, err := s.db.GetFlamegraph(runID, result.Name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err == nil {
			svg, err := generateFlamegraphSVG(ctx, fg.FoldedStacks, result.Name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if len(svg) > maxFlamegraphSize {
				http.Error(w, "flamegraph too large", http.StatusRequestEntityTooLarge)
				return
			}
			if s.svgCache != nil {
				_ = s.svgCache.Put(runID, cacheKey, svg)
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write(svg)
			return
		}
	}

	profileArtifact, err := s.db.GetArtifact(result.ID, cpuProfileKind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flamegraph not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(profileArtifact.DataBlob) > maxProfileSize {
		http.Error(w, "profile too large", http.StatusRequestEntityTooLarge)
		return
	}

	foldedStacks, err := foldedStacksFromProfile(profileArtifact.DataBlob)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	svg, err := generateFlamegraphSVG(ctx, foldedStacks, result.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(svg) > maxFlamegraphSize {
		http.Error(w, "flamegraph too large", http.StatusRequestEntityTooLarge)
		return
	}

	if s.svgCache != nil {
		_ = s.svgCache.Put(runID, cacheKey, svg)
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(svg)
}

func (s *Server) handleCallgraphSVG(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(path, "/results/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	suffix := strings.TrimSuffix(parts[1], "/callgraph")
	if suffix == parts[1] {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	resultID, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil {
		http.Error(w, "invalid result id", http.StatusBadRequest)
		return
	}

	result, err := s.db.GetResult(resultID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if result.RunID != runID {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}

	cacheKey := fmt.Sprintf("callgraph:result:%d", result.ID)
	legacyCacheKey := fmt.Sprintf("callgraph:%d", result.ID)
	if s.svgCache != nil {
		if svg, ok := s.svgCache.Get(runID, cacheKey); ok {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write(svg)
			return
		}
		if legacyCacheKey != cacheKey {
			if svg, ok := s.svgCache.Get(runID, legacyCacheKey); ok {
				_ = s.svgCache.Put(runID, cacheKey, svg)
				w.Header().Set("Content-Type", "image/svg+xml")
				_, _ = w.Write(svg)
				return
			}
		}
	}

	cached, err := s.db.GetArtifact(result.ID, callgraphSVGKind)
	if err == nil {
		if s.svgCache != nil {
			_ = s.svgCache.Put(runID, cacheKey, cached.DataBlob)
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(cached.DataBlob)
		return
	}

	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	profileArtifact, err := s.db.GetArtifact(result.ID, cpuProfileKind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "callgraph not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(profileArtifact.DataBlob) > maxProfileSize {
		http.Error(w, "profile too large", http.StatusRequestEntityTooLarge)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), flamegraphTimeout)
	defer cancel()

	if err := s.acquireFlamegraphSlot(ctx); err != nil {
		http.Error(w, "callgraph generation busy", http.StatusServiceUnavailable)
		return
	}
	defer s.releaseFlamegraphSlot()

	svg, err := generateCallgraphSVG(ctx, profileArtifact.DataBlob)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(svg) > maxFlamegraphSize {
		http.Error(w, "callgraph too large", http.StatusRequestEntityTooLarge)
		return
	}

	if s.svgCache != nil {
		_ = s.svgCache.Put(runID, cacheKey, svg)
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(svg)
}

func (s *Server) handleArtifactList(w http.ResponseWriter, r *http.Request) {
	// Path: /api/runs/{run_id}/results/{result_id}/artifacts
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/artifacts")
	parts := strings.Split(path, "/results/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	resultID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		http.Error(w, "invalid result id", http.StatusBadRequest)
		return
	}

	if err := s.ensureResultBelongsToRun(runID, resultID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	artifacts, err := s.db.ListArtifactsForResult(resultID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type artifactResponse struct {
		Kind      string `json:"kind"`
		Size      int    `json:"size"`
		CreatedAt string `json:"created_at"`
	}

	var response []artifactResponse
	for _, a := range artifacts {
		response = append(response, artifactResponse{
			Kind:      a.Kind,
			Size:      int(a.DataSize),
			CreatedAt: a.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	// Path: /api/runs/{run_id}/results/{result_id}/artifacts/{kind}/download
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/download")
	parts := strings.Split(path, "/results/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	// parts[1] is {result_id}/artifacts/{kind}
	subParts := strings.Split(parts[1], "/artifacts/")
	if len(subParts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	resultID, err := strconv.ParseInt(subParts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid result id", http.StatusBadRequest)
		return
	}

	result, err := s.db.GetResult(resultID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if result.RunID != runID {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}

	kind := subParts[1]

	artifact, err := s.db.GetArtifact(resultID, kind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "artifact not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sanitize := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}
	sanitizedName := strings.Map(sanitize, result.Name)
	sanitizedKind := strings.Map(sanitize, kind)
	if sanitizedKind == "" {
		sanitizedKind = "artifact"
	}
	filename := fmt.Sprintf("%s_%d_%d.%s", sanitizedName, runID, resultID, sanitizedKind)
	filename = filepath.Base(filepath.Clean(filename))

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	_, _ = w.Write(artifact.DataBlob)
}

func (s *Server) ensureResultBelongsToRun(runID int64, resultID int64) error {
	var actualRunID int64
	err := s.db.QueryRow(`SELECT run_id FROM results WHERE id = ?`, resultID).Scan(&actualRunID)
	if err != nil {
		return err
	}
	if actualRunID != runID {
		return sql.ErrNoRows
	}
	return nil
}

// Default parameters for regression detection
const (
	defaultWindow               = 30
	defaultMinPoints            = 5
	defaultBaselineOffset       = 3
	defaultFDR                  = 0.01
	defaultMinAbsoluteNs        = 5000.0
	regressionAlgorithmVersion  = "v7-phase6-calibration-rollout"
	regressionMetric            = "log(avg_ns)"
	regressionEstimator         = "historical_log_mean_prediction"
	regressionCohortPolicy      = "phase2_exact_identity_compatible_as_of"
	regressionFamilyDefinition  = "one_slowdown_hypothesis_per_eligible_benchmark_complete_snapshot"
	regressionCalibrationStatus = "uncalibrated_regression_score"
	regressionCalibrationCaveat = "Uncalibrated regression scores only. No p-value calibration or FDR guarantee is claimed; the frozen Phase 6 replay lacks adequate unchanged-commit and transition evidence. Run `bench calibrate` for the current data report."
	broadShiftMinBenchmarks     = 50
	broadShiftMinPositiveShare  = 0.75
	broadShiftMinGeoIncreasePct = 10.0
	changePointMinSegment       = 5
	changePointAlpha            = 0.05
	changePointPerms            = 199
	changePointMaxAgeRuns       = 2
)

func (s *Server) handleDatabaseDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.databaseDownloadSem != nil {
		select {
		case s.databaseDownloadSem <- struct{}{}:
			defer func() { <-s.databaseDownloadSem }()
		default:
			http.Error(w, "A database export is already in progress", http.StatusTooManyRequests)
			return
		}
	}

	tmpDir, err := os.MkdirTemp("", "opentui-bench-export-*")
	if err != nil {
		http.Error(w, "Failed to prepare database export", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpPath := filepath.Join(tmpDir, "bench.db")

	if err := s.db.CompactBackup(r.Context(), tmpPath); err != nil {
		if r.Context().Err() == nil {
			http.Error(w, "Failed to snapshot database", http.StatusInternalServerError)
		}
		return
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		http.Error(w, "Failed to open database export", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "Failed to stat database export", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="bench.db"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "bench.db", stat.ModTime(), f)
}

func (s *Server) handleRegressions(w http.ResponseWriter, r *http.Request) {
	if kind := r.URL.Query().Get("benchmark_kind"); kind != "" && kind != "zig" {
		http.Error(w, "regression analysis is available only for Zig benchmarks", http.StatusBadRequest)
		return
	}
	if raw := r.URL.Query().Get("method"); raw != "" {
		http.Error(w, "method parameter is no longer supported", http.StatusBadRequest)
		return
	}
	if raw := r.URL.Query().Get("df_mode"); raw != "" {
		http.Error(w, "df_mode is no longer supported; degrees of freedom are always history_count - 1", http.StatusBadRequest)
		return
	}
	window := defaultWindow
	if raw := r.URL.Query().Get("window"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			window = n
		}
	}
	minPoints := defaultMinPoints
	if raw := r.URL.Query().Get("min_points"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 {
			http.Error(w, "min_points must be at least 2", http.StatusBadRequest)
			return
		}
		minPoints = n
	}
	baselineOffset := defaultBaselineOffset
	if raw := r.URL.Query().Get("baseline_offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			baselineOffset = n
		}
	}

	// Parse optional branch parameter (defaults to "main").
	// When no run_id is given, this selects the latest run on that branch.
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}

	// Parse optional run_id parameter (defaults to latest run on branch)
	var runID int64
	if idStr := r.URL.Query().Get("run_id"); idStr != "" {
		var err error
		runID, err = strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid run_id", http.StatusBadRequest)
			return
		}
	} else {
		latestRun, err := s.db.GetLatestRunFiltered(branch, db.RunFilter{BenchmarkKind: "zig"})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No runs yet for this branch, return empty response
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"run_id":               nil,
					"branch":               branch,
					"window":               window,
					"compared_runs":        0,
					"min_points":           minPoints,
					"effective_min_points": minPoints,
					"baseline_offset":      baselineOffset,
					"algorithm_version":    regressionAlgorithmVersion,
					"metric":               regressionMetric,
					"estimator":            regressionEstimator,
					"cohort_policy":        regressionCohortPolicy,
					"family_definition":    regressionFamilyDefinition,
					"calibration_status":   regressionCalibrationStatus,
					"calibration_caveat":   regressionCalibrationCaveat,
					"fdr_level":            defaultFDR,
					"insufficient_history": true,
					"insufficient_reason":  "no_runs_for_branch",
					"total_benchmarks":     0,
					"analyzed_benchmarks":  0,
					"exclusion_counts":     map[string]int{"no_runs_for_branch": 1},
					"broad_shift":          broadshift.Empty(),
					"regressions":          []interface{}{},
				})

				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runID = latestRun.ID
	}

	// Get comparable runs window.
	// For feature branches, use main history as the baseline so we can detect
	// regressions even if the branch only has one run.
	targetRun, err := s.db.GetRun(runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if targetRun.BenchmarkKind != "zig" {
		http.Error(w, "regression analysis is available only for Zig benchmarks", http.StatusBadRequest)
		return
	}
	branch = targetRun.Branch
	if branch == "" {
		branch = "main"
	}

	var runs []db.Run
	isFeatureBranch := branch != "main"

	if isFeatureBranch {
		mainRuns, err := s.db.GetComparableMainRunsWindow(runID, window+baselineOffset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runs = append([]db.Run{*targetRun}, mainRuns...)
	} else {
		var err error
		runs, err = s.db.GetComparableRunsWindow(runID, window+baselineOffset+1)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if len(runs) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"run_id":               runID,
			"branch":               branch,
			"window":               window,
			"min_points":           minPoints,
			"baseline_offset":      baselineOffset,
			"insufficient_history": true,
			"regressions":          []interface{}{},
		})
		return
	}

	// Collect run IDs
	runIDs := make([]int64, len(runs))
	runByID := make(map[int64]db.Run)
	for i, run := range runs {
		runIDs[i] = run.ID
		runByID[run.ID] = run
	}

	// Get all benchmark names across these runs
	benchmarkKeys, err := s.db.GetDistinctBenchmarkKeys(runIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	latestRunID := runID
	broadShift := broadshift.Empty()
	resultsCache := make(map[int64][]db.Result)
	getResults := func(runID int64) ([]db.Result, error) {
		if cached, ok := resultsCache[runID]; ok {
			return cached, nil
		}
		fetched, err := s.db.GetResultSummariesForRun(runID)
		if err != nil {
			return nil, err
		}
		resultsCache[runID] = fetched
		return fetched, nil
	}
	computeBroadShift := func(newerRunID int64, olderRunID int64) (broadshift.Incident, error) {
		newerResults, err := getResults(newerRunID)
		if err != nil {
			return broadshift.Incident{}, err
		}
		olderResults, err := getResults(olderRunID)
		if err != nil {
			return broadshift.Incident{}, err
		}
		return broadshift.Detect(newerResults, olderResults, broadshift.Config{
			MinBenchmarks:    broadShiftMinBenchmarks,
			MinPositiveShare: broadShiftMinPositiveShare,
			MinGeometricPct:  broadShiftMinGeoIncreasePct,
		}), nil
	}
	if len(runs) >= 2 {
		incident, err := computeBroadShift(runs[0].ID, runs[1].ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		broadShift = incident
	}

	type changePointDiagnostic struct {
		RunID         int64   `json:"run_id"`
		PValue        float64 `json:"p_value"`
		EffectPercent float64 `json:"effect_percent"`
		MagnitudeNs   float64 `json:"magnitude_ns"`
		Recent        bool    `json:"recent"`
	}
	type regression struct {
		Name                   string                 `json:"name"`
		Category               string                 `json:"category"`
		LatestResultID         int64                  `json:"latest_result_id"`
		LatestCILowerNs        int64                  `json:"latest_ci_lower_ns"`
		LatestCIUpperNs        int64                  `json:"latest_ci_upper_ns"`
		BaselineRunID          int64                  `json:"baseline_run_id"`
		BaselineCommitHash     string                 `json:"baseline_commit_hash"`
		BaselineCommitHashFull string                 `json:"baseline_commit_hash_full"`
		BaselineCILowerNs      int64                  `json:"baseline_ci_lower_ns"`
		BaselineCIUpperNs      int64                  `json:"baseline_ci_upper_ns"`
		ChangePercent          float64                `json:"change_percent"`
		AbsoluteChangeNs       float64                `json:"absolute_change_ns"`
		BaselineNs             float64                `json:"baseline_ns"`
		MinEffectPercent       float64                `json:"min_effect_percent"`
		PValue                 *float64               `json:"p_value,omitempty"`
		AdjustedPValue         *float64               `json:"adjusted_p_value,omitempty"`
		DetectionMethod        string                 `json:"detection_method"`
		TScore                 *float64               `json:"t_score,omitempty"`
		DegreesOfFreedom       int                    `json:"degrees_of_freedom"`
		ChangePointDiagnostic  *changePointDiagnostic `json:"change_point_diagnostic,omitempty"`
	}
	type regressionsResponse struct {
		RunID               *int64              `json:"run_id"`
		Branch              string              `json:"branch"`
		Window              int                 `json:"window"`
		ComparedRuns        int                 `json:"compared_runs"`
		MinPoints           int                 `json:"min_points"`
		EffectiveMinPoints  int                 `json:"effective_min_points"`
		BaselineOffset      int                 `json:"baseline_offset"`
		AlgorithmVersion    string              `json:"algorithm_version"`
		Metric              string              `json:"metric"`
		Estimator           string              `json:"estimator"`
		CohortPolicy        string              `json:"cohort_policy"`
		FamilyDefinition    string              `json:"family_definition"`
		CalibrationStatus   string              `json:"calibration_status"`
		CalibrationCaveat   string              `json:"calibration_caveat"`
		FDRLevel            float64             `json:"fdr_level"`
		HypothesisCount     int                 `json:"hypothesis_count"`
		TotalBenchmarks     int                 `json:"total_benchmarks"`
		AnalyzedBenchmarks  int                 `json:"analyzed_benchmarks"`
		InsufficientHistory bool                `json:"insufficient_history"`
		InsufficientReason  string              `json:"insufficient_reason,omitempty"`
		ExclusionCounts     map[string]int      `json:"exclusion_counts,omitempty"`
		BroadShift          broadshift.Incident `json:"broad_shift"`
		Regressions         []regression        `json:"regressions"`
	}

	type benchResult struct {
		testResult stats.RegressionResult
		latest     db.Result
		baseline   *stats.BaselineStats
		trends     []struct {
			Run    db.Run
			Result db.Result
		}
	}

	var regressions []regression
	var benchResults []benchResult
	analyzableBenchmarks := 0
	exclusionCounts := map[string]int{}
	incrementExclusion := func(reason string) {
		exclusionCounts[reason]++
	}
	effectiveMinPoints := minPoints

	// Analyze each benchmark
	for _, benchmarkKey := range benchmarkKeys {
		// Get results for this benchmark across all runs
		resultsMap, err := s.db.GetResultsForBenchmarkInRuns(benchmarkKey, runIDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Check if latest run has this benchmark
		latestResult, hasLatest := resultsMap[latestRunID]
		if !hasLatest {
			incrementExclusion("missing_latest")
			continue
		}
		if latestResult.AvgNs <= 0 {
			incrementExclusion("invalid_latest_avg")
			continue
		}
		benchmarkTrends, err := s.db.GetTrend(latestResult.ID, window+baselineOffset+1)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		toRunStat := func(runID int64, result db.Result) stats.RunStat {
			return stats.RunStat{RunID: runID, Avg: float64(result.AvgNs)}
		}

		var observations []stats.OrderedRunStat
		var targetObservation stats.OrderedRunStat
		for _, trend := range benchmarkTrends {
			run := trend.Run
			result := trend.Result
			runDate, err := time.Parse(time.RFC3339Nano, run.RunDate)
			if err != nil {
				http.Error(w, "invalid run date: "+err.Error(), http.StatusInternalServerError)
				return
			}
			observation := stats.OrderedRunStat{RunDate: runDate, Stat: toRunStat(run.ID, result)}
			observations = append(observations, observation)
			runByID[run.ID] = run
			if run.ID == latestRunID {
				targetObservation = observation
			}
		}

		evaluation := stats.EvaluateSnapshot(targetObservation, observations, stats.SnapshotConfig{
			Window: window, MinPoints: minPoints, BaselineOffset: baselineOffset,
		})
		if evaluation.Baseline == nil {
			incrementExclusion("history_too_short_or_invalid")
			continue
		}
		baseline := evaluation.Baseline
		analyzableBenchmarks++

		result := evaluation.Result
		benchResults = append(benchResults, benchResult{
			testResult: result,
			latest:     latestResult,
			baseline:   baseline,
			trends:     benchmarkTrends,
		})
	}

	if len(benchResults) > 0 {
		pValues := make([]float64, len(benchResults))
		for i, bench := range benchResults {
			pValues[i] = *bench.testResult.PValue
		}
		for _, bh := range stats.BenjaminiHochberg(pValues, defaultFDR) {
			bench := benchResults[bh.Index]
			changePercent := *bench.testResult.ChangePercent
			absoluteChange := *bench.testResult.AbsoluteChangeNs
			if !bh.IsSignificant || changePercent < stats.MinPracticalRegressionEffectPercent || absoluteChange < defaultMinAbsoluteNs {
				continue
			}
			var diagnostic *changePointDiagnostic
			if len(bench.trends) >= 2*changePointMinSegment {
				series := make([]stats.RunStat, 0, len(bench.trends))
				for i := len(bench.trends) - 1; i >= 0; i-- {
					trend := bench.trends[i]
					series = append(series, stats.RunStat{RunID: trend.Run.ID, Avg: float64(trend.Result.AvgNs)})
				}
				points := stats.DetectChangePoints(series, changePointMinSegment, changePointAlpha, changePointPerms)
				if len(points) > 0 {
					point := points[len(points)-1]
					age := len(bench.trends)
					for i, trend := range bench.trends {
						if trend.Run.ID == point.RunID {
							age = i
							break
						}
					}
					diagnostic = &changePointDiagnostic{
						RunID: point.RunID, PValue: point.PValue, EffectPercent: point.EffectPercent,
						MagnitudeNs: point.Magnitude, Recent: age <= changePointMaxAgeRuns,
					}
				}
			}

			ciLower, ciUpper, _ := stats.CI95(bench.latest.AvgNs, bench.latest.StdDevNs, bench.latest.SampleCount)
			rawPValue := *bench.testResult.PValue
			adjustedPValue := bh.AdjPValue
			var tScore *float64
			if !math.IsInf(*bench.testResult.TScore, 0) {
				tScore = bench.testResult.TScore
			}

			reg := regression{
				Name:                  bench.latest.Name,
				Category:              bench.latest.Category,
				LatestResultID:        bench.latest.ID,
				LatestCILowerNs:       ciLower,
				LatestCIUpperNs:       ciUpper,
				BaselineRunID:         bench.baseline.RunID,
				BaselineCILowerNs:     int64(bench.baseline.CILower),
				BaselineCIUpperNs:     int64(bench.baseline.CIUpper),
				ChangePercent:         changePercent,
				AbsoluteChangeNs:      absoluteChange,
				BaselineNs:            bench.baseline.BaselineNs,
				MinEffectPercent:      bench.testResult.MinEffectPercent,
				PValue:                &rawPValue,
				AdjustedPValue:        &adjustedPValue,
				DetectionMethod:       "log_avg_prediction_score",
				TScore:                tScore,
				DegreesOfFreedom:      bench.testResult.DegreesOfFreedom,
				ChangePointDiagnostic: diagnostic,
			}

			if baselineRun, ok := runByID[bench.baseline.RunID]; ok {
				reg.BaselineCommitHash = baselineRun.CommitHash
				reg.BaselineCommitHashFull = baselineRun.CommitHashFull
			}

			regressions = append(regressions, reg)
		}
	}

	// Use branch from comparable runs if available (normalizes legacy empty values).
	if len(runs) > 0 && runs[0].Branch != "" {
		branch = runs[0].Branch
	}

	response := regressionsResponse{
		RunID:               &runID,
		Branch:              branch,
		Window:              window,
		ComparedRuns:        len(runs),
		MinPoints:           minPoints,
		EffectiveMinPoints:  effectiveMinPoints,
		BaselineOffset:      baselineOffset,
		AlgorithmVersion:    regressionAlgorithmVersion,
		Metric:              regressionMetric,
		Estimator:           regressionEstimator,
		CohortPolicy:        regressionCohortPolicy,
		FamilyDefinition:    regressionFamilyDefinition,
		CalibrationStatus:   regressionCalibrationStatus,
		CalibrationCaveat:   regressionCalibrationCaveat,
		FDRLevel:            defaultFDR,
		HypothesisCount:     len(benchResults),
		TotalBenchmarks:     len(benchmarkKeys),
		AnalyzedBenchmarks:  analyzableBenchmarks,
		InsufficientHistory: analyzableBenchmarks == 0,
		ExclusionCounts:     exclusionCounts,
		BroadShift:          broadShift,
		Regressions:         regressions,
	}
	if analyzableBenchmarks == 0 {
		topReason := ""
		topCount := -1
		for reason, count := range exclusionCounts {
			if count > topCount {
				topReason = reason
				topCount = count
			}
		}
		response.InsufficientReason = topReason
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	filter, err := runFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	branches, err := s.db.GetBranchesWithRunsFiltered(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(branches)
}

// --- Job endpoints ---

type jobResponse struct {
	runIdentityResponse
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Kind        string `json:"kind"`
	Branch      string `json:"branch"`
	CommitHash  string `json:"commit_hash,omitempty"`
	RepoURL     string `json:"repo_url"`
	Samples     int    `json:"samples"`
	Profile     string `json:"profile"`
	Notes       string `json:"notes,omitempty"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
	RunID       *int64 `json:"run_id,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
}

func jobToResponse(j *db.Job) jobResponse {
	return jobResponse{
		runIdentityResponse: runIdentityResponse{
			BenchmarkKind: j.BenchmarkKind, BenchmarkSuite: j.BenchmarkSuite,
			ProtocolVersion: j.ProtocolVersion, ManifestHash: j.ManifestHash,
			JSRuntime: j.JSRuntime, RuntimeVersion: j.RuntimeVersion,
		},
		ID:          j.ID,
		Status:      j.Status,
		Kind:        j.Kind,
		Branch:      j.Branch,
		CommitHash:  j.CommitHash,
		RepoURL:     j.RepoURL,
		Samples:     j.Samples,
		Profile:     j.Profile,
		Notes:       j.Notes,
		CreatedAt:   j.CreatedAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		Error:       j.Error,
		RunID:       j.RunID,
		RequestedBy: j.RequestedBy,
	}
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Branch          string `json:"branch"`
		CommitHash      string `json:"commit_hash"`
		Samples         int    `json:"samples"`
		Profile         string `json:"profile"`
		Notes           string `json:"notes"`
		RequestedBy     string `json:"requested_by"`
		BenchmarkKind   string `json:"benchmark_kind"`
		BenchmarkSuite  string `json:"benchmark_suite"`
		ProtocolVersion int64  `json:"protocol_version"`
		ManifestHash    string `json:"manifest_hash"`
		JSRuntime       string `json:"js_runtime"`
		RuntimeVersion  string `json:"runtime_version"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
		return
	}

	if req.Branch == "" {
		http.Error(w, "branch is required", http.StatusBadRequest)
		return
	}
	if len(req.Branch) > 512 || len(req.CommitHash) > 128 || len(req.Notes) > 4096 || len(req.RequestedBy) > 256 {
		http.Error(w, "job field exceeds maximum length", http.StatusBadRequest)
		return
	}
	if req.BenchmarkKind == "" {
		req.BenchmarkKind = "zig"
	}
	if req.BenchmarkSuite == "" {
		req.BenchmarkSuite = jsbench.Suite
	}
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = 1
	}
	if req.BenchmarkKind != "zig" && req.BenchmarkKind != jsbench.Kind {
		http.Error(w, "benchmark_kind must be 'zig' or 'js'", http.StatusBadRequest)
		return
	}
	if req.BenchmarkKind == jsbench.Kind && !s.javascriptRuns {
		http.Error(w, "JavaScript benchmark runs are disabled", http.StatusForbidden)
		return
	}
	if req.BenchmarkKind == jsbench.Kind {
		if req.JSRuntime == "" {
			req.JSRuntime = jsbench.RuntimeBun
		}
		if req.RuntimeVersion == "" {
			req.RuntimeVersion = jsbench.RuntimeVersion(req.JSRuntime)
		}
	}

	if req.Samples <= 0 {
		req.Samples = 3
	}
	if req.Profile == "" {
		if req.BenchmarkKind == jsbench.Kind {
			req.Profile = "none"
		} else {
			req.Profile = "cpu"
		}
	}
	if req.Profile != "none" && req.Profile != "cpu" {
		http.Error(w, "profile must be 'none' or 'cpu'", http.StatusBadRequest)
		return
	}
	if req.BenchmarkKind == jsbench.Kind && !jsbench.MatchesRuntimeJob(req.BenchmarkSuite, req.ProtocolVersion,
		req.JSRuntime, req.RuntimeVersion, req.ManifestHash, req.Samples, req.Profile) {
		http.Error(w, "JavaScript jobs require canonical suite, protocol, runtime, manifest_hash, samples=3, and profile='none'", http.StatusBadRequest)
		return
	}
	if req.BenchmarkKind == "zig" && req.ManifestHash != "" {
		http.Error(w, "Zig jobs must not include manifest_hash", http.StatusBadRequest)
		return
	}

	// Parse "user:branch" format (GitHub fork reference).
	// e.g. "zenyr:fix/255-line-starts-byte-offset" becomes:
	//   repo_url = "https://github.com/zenyr/opentui.git"
	//   branch   = "fix/255-line-starts-byte-offset"
	repoURL := "origin"
	branch := req.Branch
	if parts := strings.SplitN(req.Branch, ":", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		repoURL = "https://github.com/" + parts[0] + "/opentui.git"
		branch = parts[1]
	}

	job := &db.Job{
		Status:        "pending",
		Kind:          "benchmark",
		Branch:        branch,
		CommitHash:    req.CommitHash,
		RepoURL:       repoURL,
		Samples:       req.Samples,
		Profile:       req.Profile,
		Notes:         req.Notes,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		RequestedBy:   req.RequestedBy,
		BenchmarkKind: req.BenchmarkKind, BenchmarkSuite: req.BenchmarkSuite,
		ProtocolVersion: req.ProtocolVersion, ManifestHash: req.ManifestHash,
		JSRuntime: req.JSRuntime, RuntimeVersion: req.RuntimeVersion,
	}

	id, err := s.db.InsertJob(job)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	created, err := s.db.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(jobToResponse(created)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	status := r.URL.Query().Get("status")
	branch := r.URL.Query().Get("branch")

	var protocolVersion int64
	if value := r.URL.Query().Get("protocol_version"); value != "" {
		var err error
		protocolVersion, err = strconv.ParseInt(value, 10, 64)
		if err != nil || protocolVersion <= 0 {
			http.Error(w, "invalid protocol_version", http.StatusBadRequest)
			return
		}
	}
	jobs, err := s.db.ListJobsFiltered(limit, status, branch,
		r.URL.Query().Get("benchmark_kind"), r.URL.Query().Get("requested_by"),
		r.URL.Query().Get("benchmark_suite"), protocolVersion, r.URL.Query().Get("manifest_hash"),
		r.URL.Query().Get("js_runtime"), r.URL.Query().Get("runtime_version"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var response []jobResponse
	for _, j := range jobs {
		response = append(response, jobToResponse(&j))
	}

	// Return empty array instead of null
	if response == nil {
		response = []jobResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleJobsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListJobs(w, r)
	case http.MethodPost:
		s.requireAuth(s.handleCreateJob)(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) routeJobsAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	if path == "" {
		http.Error(w, "job id required", http.StatusBadRequest)
		return
	}

	// Handle /api/jobs/claim
	if path == "claim" {
		s.requireAuth(s.handleClaimJob)(w, r)
		return
	}

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetJob(w, r, id)
	case http.MethodPatch:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			s.handleUpdateJob(w, r, id)
		})(w, r)
	case http.MethodDelete:
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			s.handleCancelJob(w, r, id)
		})(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, _ *http.Request, id int64) {
	job, err := s.db.GetJob(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobToResponse(job)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleCancelJob(w http.ResponseWriter, _ *http.Request, id int64) {
	err := s.db.CancelJob(id)
	if err != nil {
		if strings.Contains(err.Error(), "not pending") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	job, err := s.db.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobToResponse(job)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- New write API endpoints ---

// handleCreateRun handles POST /api/runs - creates a run with all results and mem_stats.
func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CommitHash      string            `json:"commit_hash"`
		CommitHashFull  string            `json:"commit_hash_full"`
		CommitMessage   string            `json:"commit_message"`
		CommitDate      string            `json:"commit_date"`
		Branch          string            `json:"branch"`
		MachineID       string            `json:"machine_id"`
		Notes           string            `json:"notes"`
		ZigOptimize     string            `json:"zig_optimize"`
		BenchmarkKind   string            `json:"benchmark_kind"`
		BenchmarkSuite  string            `json:"benchmark_suite"`
		ProtocolVersion int64             `json:"protocol_version"`
		BunVersion      string            `json:"bun_version"`
		JSRuntime       string            `json:"js_runtime"`
		RuntimeVersion  string            `json:"runtime_version"`
		ZigVersion      string            `json:"zig_version"`
		ManifestHash    string            `json:"manifest_hash"`
		ManifestJSON    string            `json:"manifest_json"`
		Results         []createRunResult `json:"results"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
		return
	}

	if req.CommitHash == "" {
		writeJSONError(w, "commit_hash is required", http.StatusBadRequest)
		return
	}

	if req.BenchmarkKind == "" {
		req.BenchmarkKind = "zig"
	}
	if req.BenchmarkKind == jsbench.Kind && !s.javascriptRuns {
		writeJSONError(w, "JavaScript benchmark runs are disabled", http.StatusForbidden)
		return
	}
	legacyJSIdentity := req.BenchmarkKind == jsbench.Kind && req.JSRuntime == "" && req.RuntimeVersion == ""
	if req.BenchmarkKind == jsbench.Kind {
		if req.JSRuntime == "" {
			req.JSRuntime = jsbench.RuntimeBun
		}
		if req.RuntimeVersion == "" {
			if req.BunVersion != "" {
				req.RuntimeVersion = req.BunVersion
			} else {
				req.RuntimeVersion = jsbench.RuntimeVersion(req.JSRuntime)
			}
		}
		if req.JSRuntime == jsbench.RuntimeBun && req.BunVersion == "" {
			req.BunVersion = req.RuntimeVersion
		}
	}
	if req.BenchmarkKind == "zig" && req.ZigOptimize == "" {
		req.ZigOptimize = "ReleaseFast"
	}
	if req.BenchmarkSuite == "" {
		req.BenchmarkSuite = "core-default"
	}
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = 1
	}

	run := &db.Run{
		CommitHash:     req.CommitHash,
		CommitHashFull: req.CommitHashFull,
		CommitMessage:  req.CommitMessage,
		CommitDate:     req.CommitDate,
		Branch:         req.Branch,
		RunDate:        time.Now().UTC().Format(time.RFC3339),
		MachineID:      req.MachineID,
		Notes:          req.Notes,
		ZigOptimize:    req.ZigOptimize,
		BenchmarkKind:  req.BenchmarkKind, BenchmarkSuite: req.BenchmarkSuite,
		ProtocolVersion: req.ProtocolVersion, BunVersion: req.BunVersion,
		JSRuntime: req.JSRuntime, RuntimeVersion: req.RuntimeVersion,
		ZigVersion: req.ZigVersion, ManifestHash: req.ManifestHash, ManifestJSON: req.ManifestJSON,
		LegacyJSIdentity: legacyJSIdentity,
	}
	if err := validateCreateRun(run, req.Results); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	results := make([]db.Result, 0, len(req.Results))
	for _, rr := range req.Results {
		result := db.Result{
			Category:             rr.Category,
			Name:                 rr.Name,
			MinNs:                rr.MinNs,
			AvgNs:                rr.AvgNs,
			MaxNs:                rr.MaxNs,
			StdDevNs:             rr.StdDevNs,
			P50Ns:                rr.P50Ns,
			P95Ns:                rr.P95Ns,
			P99Ns:                rr.P99Ns,
			TotalNs:              rr.TotalNs,
			Iterations:           rr.Iterations,
			SampleCount:          rr.SampleCount,
			SampleAvgVarianceNs2: rr.SampleAvgVarianceNs2,
			Samples:              rr.Samples,
		}
		// Missing fields identify old clients and deliberately retain legacy
		// provenance rather than inferring samples from aggregate summaries.
		if rr.SampleDataVersion != nil {
			result.SampleDataVersion = *rr.SampleDataVersion
		}
		if rr.SummaryVersion != nil {
			result.SummaryVersion = *rr.SummaryVersion
		} else {
			result.SummaryVersion = 1
		}
		for _, ms := range rr.MemStats {
			result.MemStats = append(result.MemStats, db.MemStat{
				StatName: ms.Name,
				Bytes:    ms.Bytes,
			})
		}
		results = append(results, result)
	}

	storedRun, resultIDs, created, err := s.db.InsertRunWithResultsIfAbsent(run, results)
	if err != nil {
		writeJSONError(w, "insert run: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":               storedRun.ID,
		"commit_hash":      storedRun.CommitHash,
		"commit_hash_full": storedRun.CommitHashFull,
		"run_date":         storedRun.RunDate,
		"result_count":     len(resultIDs),
		"result_ids":       legacyResultIDMap(resultIDs),
		"results":          resultIDList(resultIDs),
		"benchmark_kind":   storedRun.BenchmarkKind,
		"benchmark_suite":  storedRun.BenchmarkSuite,
		"protocol_version": storedRun.ProtocolVersion,
		"bun_version":      storedRun.BunVersion,
		"js_runtime":       storedRun.JSRuntime,
		"runtime_version":  storedRun.RuntimeVersion,
		"zig_version":      storedRun.ZigVersion,
		"manifest_hash":    storedRun.ManifestHash,
	})
}

// legacyResultIDMap is retained for already deployed record clients. New
// clients use the structured results list and never parse this encoding.
func legacyResultIDMap(resultIDs map[db.BenchmarkKey]int64) map[string]int64 {
	legacy := make(map[string]int64, len(resultIDs))
	ambiguous := make(map[string]bool)
	nameCounts := make(map[string]int, len(resultIDs))
	for key := range resultIDs {
		nameCounts[key.Name]++
	}
	for key, id := range resultIDs {
		// Deployed clients selected profile artifacts by name alone. Omitting
		// duplicate names makes those clients fail safely instead of misrouting.
		if nameCounts[key.Name] > 1 {
			continue
		}
		encoded := key.Category + "/" + key.Name
		if _, exists := legacy[encoded]; exists {
			delete(legacy, encoded)
			ambiguous[encoded] = true
			continue
		}
		if !ambiguous[encoded] {
			legacy[encoded] = id
		}
	}
	return legacy
}

func resultIDList(resultIDs map[db.BenchmarkKey]int64) []struct {
	ID       int64  `json:"id"`
	Category string `json:"category"`
	Name     string `json:"name"`
} {
	results := make([]struct {
		ID       int64  `json:"id"`
		Category string `json:"category"`
		Name     string `json:"name"`
	}, 0, len(resultIDs))
	for key, id := range resultIDs {
		results = append(results, struct {
			ID       int64  `json:"id"`
			Category string `json:"category"`
			Name     string `json:"name"`
		}{ID: id, Category: key.Category, Name: key.Name})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Category == results[j].Category {
			return results[i].Name < results[j].Name
		}
		return results[i].Category < results[j].Category
	})
	return results
}

// handleUploadArtifact handles POST /api/runs/{id}/results/{rid}/artifacts
func (s *Server) handleUploadArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse path: /api/runs/{id}/results/{rid}/artifacts
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/artifacts")
	parts := strings.Split(path, "/results/")
	if len(parts) != 2 {
		writeJSONError(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}

	resultID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		writeJSONError(w, "invalid result id", http.StatusBadRequest)
		return
	}

	// Validate result belongs to run
	if err := s.ensureResultBelongsToRun(runID, resultID); err != nil {
		if err == sql.ErrNoRows {
			writeJSONError(w, "result not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	kind := r.URL.Query().Get("kind")
	if kind == "" {
		writeJSONError(w, "kind query parameter required", http.StatusBadRequest)
		return
	}
	if kind != cpuProfileKind {
		writeJSONError(w, "unsupported artifact kind", http.StatusBadRequest)
		return
	}

	metadata := r.URL.Query().Get("metadata")
	if metadata == "" {
		metadata = "{}"
	}

	// Reject oversized profiles instead of silently storing a truncated body.
	r.Body = http.MaxBytesReader(w, r.Body, maxProfileSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSONError(w, "profile too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeJSONError(w, "read body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(data) == 0 {
		writeJSONError(w, "empty body", http.StatusBadRequest)
		return
	}

	err = s.db.InsertArtifactIfMissing(&db.Artifact{
		ResultID:  resultID,
		Kind:      kind,
		DataBlob:  data,
		Metadata:  metadata,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		writeJSONError(w, "insert artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"kind": kind,
		"size": len(data),
	})
}

// handleFinalizeArtifacts applies retention after all artifacts for a run have
// been uploaded, so profile sets are retained or discarded as a complete run.
func (s *Server) handleFinalizeArtifacts(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/artifacts/finalize")
	runID, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}
	if _, err := s.db.GetRun(runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, "run not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pruned, complete, err := s.db.FinalizeProfileData(runID, s.profileRetentionConfig())
	if err != nil {
		writeJSONError(w, "prune profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"complete":       complete,
		"deleted":        pruned.BlobsDeleted,
		"bytes_deleted":  pruned.BytesDeleted,
		"retained_runs":  pruned.ProfileRunsRetained,
		"bytes_retained": pruned.BytesRetained,
	})
}

// handleHasCommit handles GET /api/has-commit/{hash}
func (s *Server) handleHasCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hash := strings.TrimPrefix(r.URL.Path, "/api/has-commit/")
	if hash == "" {
		writeJSONError(w, "commit hash required", http.StatusBadRequest)
		return
	}

	filter, err := runFilterFromRequest(r)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	exists, err := s.db.HasCommitFiltered(hash, filter)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"exists": exists,
	})
}

// handleLatestCommit handles GET /api/latest-commit
// Accepts optional ?branch= query parameter to filter by branch.
func (s *Server) handleLatestCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filter, err := runFilterFromRequest(r)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := s.db.GetLatestRunFiltered(r.URL.Query().Get("branch"), filter)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"commit_hash":      nil,
				"commit_hash_full": nil,
			})
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"commit_hash":      run.CommitHash,
		"commit_hash_full": run.CommitHashFull,
		"benchmark_kind":   run.BenchmarkKind, "benchmark_suite": run.BenchmarkSuite,
		"protocol_version": run.ProtocolVersion, "bun_version": run.BunVersion,
		"js_runtime": run.JSRuntime, "runtime_version": run.RuntimeVersion,
		"zig_version": run.ZigVersion, "manifest_hash": run.ManifestHash,
		"manifest_json": run.ManifestJSON, "machine_id": run.MachineID,
		"zig_optimize": run.ZigOptimize,
	})
}

// handleClaimJob handles POST /api/jobs/claim - atomically claim next pending job.
func (s *Server) handleClaimJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get(joblease.QueryParameter) != strconv.Itoa(joblease.Protocol) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var claimRequest struct {
		ClaimToken string `json:"claim_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claimRequest); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
		return
	}
	if err := joblease.ValidateToken(claimRequest.ClaimToken); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	benchmarkKind := r.URL.Query().Get("benchmark_kind")
	if !s.javascriptRuns {
		if benchmarkKind == jsbench.Kind {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if benchmarkKind == "" {
			benchmarkKind = "zig"
		}
	}
	runtimes := strings.Split(strings.Trim(r.URL.Query().Get("javascript_runtimes"), ","), ",")
	if len(runtimes) == 1 && runtimes[0] == "" {
		runtimes = nil
	}
	job, err := s.db.ClaimNextPendingJobWithToken(benchmarkKind, claimRequest.ClaimToken, runtimes...)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobToResponse(job)); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleUpdateJob handles PATCH /api/jobs/{id} - update job status/commit/run_id.
func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ClaimToken string  `json:"claim_token"`
		Status     *string `json:"status"`
		CommitHash *string `json:"commit_hash"`
		RunID      *int64  `json:"run_id"`
		Error      *string `json:"error"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Get current job
	job, err := s.db.GetJob(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, "job not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Handle commit_hash update (no status change required)
	if req.CommitHash != nil {
		if writeJobMutationError(w, "update commit hash", s.db.UpdateJobCommitHash(r.Context(), id, req.ClaimToken, *req.CommitHash)) {
			return
		}
	}

	// Handle status transitions
	if req.Status != nil {
		newStatus := *req.Status
		currentStatus := job.Status

		switch {
		case currentStatus == "running" && newStatus == "completed":
			if req.RunID == nil {
				writeJSONError(w, "run_id required for completed status", http.StatusBadRequest)
				return
			}
			if writeJobMutationError(w, "complete job", s.db.CompleteJob(r.Context(), id, req.ClaimToken, *req.RunID)) {
				return
			}
		case currentStatus == "running" && newStatus == "failed":
			errMsg := ""
			if req.Error != nil {
				errMsg = *req.Error
			}
			if writeJobMutationError(w, "fail job", s.db.FailJob(r.Context(), id, req.ClaimToken, errMsg)) {
				return
			}
		case currentStatus == "running" && newStatus == "pending":
			if writeJobMutationError(w, "release job", s.db.ReleaseJob(r.Context(), id, req.ClaimToken)) {
				return
			}
		case currentStatus == "running" && newStatus == "running":
			// No-op for status, just allow commit_hash update
		default:
			writeJSONError(w, fmt.Sprintf("invalid state transition: %s -> %s", currentStatus, newStatus), http.StatusConflict)
			return
		}
	}

	// Re-fetch and return updated job
	updated, err := s.db.GetJob(id)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobToResponse(updated)); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJobMutationError(w http.ResponseWriter, action string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, db.ErrJobClaimLost) {
		writeJSONError(w, err.Error(), http.StatusConflict)
	} else if errors.Is(err, db.ErrJobRunMismatch) {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
	} else {
		writeJSONError(w, action+": "+err.Error(), http.StatusInternalServerError)
	}
	return true
}

// writeJSONError writes a JSON error response in the standard format.
func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
