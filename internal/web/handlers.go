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

	"opentui-bench/internal/db"
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
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	branch := r.URL.Query().Get("branch")
	since := r.URL.Query().Get("since")

	runs, err := s.db.ListRuns(limit, branch, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type runResponse struct {
		ID            int64  `json:"id"`
		CommitHash    string `json:"commit_hash"`
		CommitMessage string `json:"commit_message"`
		Branch        string `json:"branch"`
		RunDate       string `json:"run_date"`
		Notes         string `json:"notes"`
		ResultCount   int    `json:"result_count"`
	}

	var response []runResponse
	for _, run := range runs {
		count, err := s.db.CountResultsForRun(run.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response = append(response, runResponse{
			ID:            run.ID,
			CommitHash:    run.CommitHash,
			CommitMessage: run.CommitMessage,
			Branch:        run.Branch,
			RunDate:       run.RunDate,
			Notes:         run.Notes,
			ResultCount:   count,
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
		ID:            run.ID,
		CommitHash:    run.CommitHash,
		CommitMessage: run.CommitMessage,
		Branch:        run.Branch,
		RunDate:       run.RunDate,
		Notes:         run.Notes,
		Results:       resultResponses,
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

	var resultsA, resultsB []db.Result
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
		runA, err := s.db.GetRunByCommit(commitA)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run A not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runB, err := s.db.GetRunByCommit(commitB)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "run B not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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

	// Use P50Ns (median) for comparison - more robust to outliers
	resultsBMap := make(map[db.BenchmarkKey]db.Result)
	for _, r := range resultsB {
		resultsBMap[db.BenchmarkKey{Category: r.Category, Name: r.Name}] = r
	}

	type comparison struct {
		Name             string  `json:"name"`
		Category         string  `json:"category"`
		BaselineNs       int64   `json:"baseline_ns"` // Median of baseline
		CurrentNs        int64   `json:"current_ns"`  // Median of current
		ChangePercent    float64 `json:"change_percent"`
		IsRegression     bool    `json:"is_regression"`
		BaselineResultID int64   `json:"baseline_result_id"`
		CurrentResultID  int64   `json:"current_result_id"`
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
				IsRegression:     change > threshold,
				BaselineResultID: rA.ID,
				CurrentResultID:  resultB.ID,
			})
		}
	}

	response := struct {
		Baseline    string       `json:"baseline"`
		Current     string       `json:"current"`
		Comparisons []comparison `json:"comparisons"`
	}{
		Baseline:    runAHash,
		Current:     runBHash,
		Comparisons: comparisons,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	resultID, err := strconv.ParseInt(r.URL.Query().Get("result_id"), 10, 64)
	if err != nil || resultID <= 0 {
		http.Error(w, "valid result_id parameter required", http.StatusBadRequest)
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
	var targetFound bool
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
			targetFound = true
		}
	}

	var points []trendPoint
	for i, t := range trends {
		if i >= limit {
			break
		}
		ciLower, ciUpper, sem := stats.CI95(t.Result.AvgNs, t.Result.StdDevNs, t.Result.SampleCount)

		point := trendPoint{
			RunID:         t.Run.ID,
			ResultID:      t.Result.ID,
			CommitHash:    t.Run.CommitHash,
			CommitMessage: t.Run.CommitMessage,
			Branch:        t.Run.Branch,
			RunDate:       t.Run.RunDate,
			AvgNs:         t.Result.AvgNs,
			MedianNs:      t.Result.P50Ns,
			MinNs:         t.Result.MinNs,
			MaxNs:         t.Result.MaxNs,
			StdDevNs:      t.Result.StdDevNs,
			SampleCount:   t.Result.SampleCount,
			CiLowerNs:     ciLower,
			CiUpperNs:     ciUpper,
			SemNs:         sem,
		}

		points = append(points, point)
	}

	response := trendResponse{
		Points: points, AlgorithmVersion: regressionAlgorithmVersion, Metric: regressionMetric,
		Estimator: regressionEstimator, CohortPolicy: regressionCohortPolicy,
		FamilyDefinition: regressionFamilyDefinition, CalibrationStatus: regressionCalibrationStatus,
		CalibrationCaveat: regressionCalibrationCaveat, FDRLevel: defaultFDR,
	}
	if targetFound {
		evaluation := stats.EvaluateSnapshot(target, observations, stats.SnapshotConfig{
			Window: defaultWindow, MinPoints: defaultMinPoints, BaselineOffset: defaultBaselineOffset,
		})
		response.CurrentStatus.RunID = target.Stat.RunID
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
	rows, err := s.db.Query(`SELECT DISTINCT category, name FROM results ORDER BY category, name`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	var keys []db.BenchmarkKey
	for rows.Next() {
		var key db.BenchmarkKey
		if err := rows.Scan(&key.Category, &key.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		keys = append(keys, key)
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
			if err := s.db.InsertArtifactIfMissing(&db.Artifact{
				ResultID:  result.ID,
				Kind:      flamegraphSVGKind,
				DataBlob:  svg,
				Metadata:  "{}",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
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

	if err := s.db.InsertArtifactIfMissing(&db.Artifact{
		ResultID:  result.ID,
		Kind:      flamegraphSVGKind,
		DataBlob:  svg,
		Metadata:  "{}",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	if err := s.db.InsertArtifactIfMissing(&db.Artifact{
		ResultID:  result.ID,
		Kind:      callgraphSVGKind,
		DataBlob:  svg,
		Metadata:  "{}",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	defaultWindow                = 30
	defaultMinPoints             = 5
	defaultBaselineOffset        = 3
	defaultFDR                   = 0.01
	defaultMinAbsoluteNs         = 5000.0
	regressionAlgorithmVersion   = "v5-log-avg-prediction"
	regressionMetric             = "log(avg_ns)"
	regressionEstimator          = "historical_log_mean_prediction"
	regressionCohortPolicy       = "phase2_exact_identity_compatible_as_of"
	regressionFamilyDefinition   = "one_slowdown_hypothesis_per_eligible_benchmark_complete_snapshot"
	regressionCalibrationStatus  = "uncalibrated_regression_score"
	regressionCalibrationCaveat  = "Under valid p-values and BH's cross-benchmark dependence assumptions, BH controls FDR within each snapshot only; it does not control accumulated false-alert rates across sequential snapshots. Scores also assume approximately stationary independent normal log run averages."
	globalShiftMinBenchmarks     = 50
	globalShiftMinPositiveShare  = 0.75
	globalShiftMinGeoIncreasePct = 10.0
	changePointMinSegment        = 5
	changePointAlpha             = 0.05
	changePointPerms             = 199
	changePointMaxAgeRuns        = 2
)

func (s *Server) handleDatabaseDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dbPath := s.db.Path()

	// Checkpoint WAL for consistency
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	f, err := os.Open(dbPath)
	if err != nil {
		http.Error(w, "Failed to open database", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "Failed to stat database", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="bench.db"`)
	http.ServeContent(w, r, "bench.db", stat.ModTime(), f)
}

func (s *Server) handleRegressions(w http.ResponseWriter, r *http.Request) {
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
		latestRun, err := s.db.GetLatestRunForBranch(branch)
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
	globalShiftDetected := false
	globalShiftPositiveShare := 0.0
	globalShiftGeoIncreasePct := 0.0
	globalShiftComparedBenchmarks := 0
	resultsCache := make(map[int64][]db.Result)
	getResults := func(runID int64) ([]db.Result, error) {
		if cached, ok := resultsCache[runID]; ok {
			return cached, nil
		}
		fetched, err := s.db.GetResultsForRun(runID)
		if err != nil {
			return nil, err
		}
		resultsCache[runID] = fetched
		return fetched, nil
	}
	type shiftMetrics struct {
		detected      bool
		positiveShare float64
		geoPct        float64
		compared      int
	}
	computeShift := func(newerRunID int64, olderRunID int64) (shiftMetrics, error) {
		newerResults, err := getResults(newerRunID)
		if err != nil {
			return shiftMetrics{}, err
		}
		olderResults, err := getResults(olderRunID)
		if err != nil {
			return shiftMetrics{}, err
		}

		olderMap := make(map[db.BenchmarkKey]int64, len(olderResults))
		for _, r := range olderResults {
			olderMap[db.BenchmarkKey{Category: r.Category, Name: r.Name}] = r.AvgNs
		}

		positiveCount := 0
		compared := 0
		logSum := 0.0
		for _, newer := range newerResults {
			older, ok := olderMap[db.BenchmarkKey{Category: newer.Category, Name: newer.Name}]
			if !ok || older <= 0 || newer.AvgNs <= 0 {
				continue
			}
			compared++
			if newer.AvgNs > older {
				positiveCount++
			}
			logSum += math.Log(float64(newer.AvgNs) / float64(older))
		}

		if compared == 0 {
			return shiftMetrics{}, nil
		}

		positiveShare := float64(positiveCount) / float64(compared)
		geoPct := (math.Exp(logSum/float64(compared)) - 1.0) * 100.0
		detected := compared >= globalShiftMinBenchmarks &&
			positiveShare >= globalShiftMinPositiveShare &&
			geoPct >= globalShiftMinGeoIncreasePct

		return shiftMetrics{
			detected:      detected,
			positiveShare: positiveShare,
			geoPct:        geoPct,
			compared:      compared,
		}, nil
	}
	if len(runs) >= 2 {
		metrics, err := computeShift(runs[0].ID, runs[1].ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		globalShiftDetected = metrics.detected
		globalShiftPositiveShare = metrics.positiveShare
		globalShiftGeoIncreasePct = metrics.geoPct
		globalShiftComparedBenchmarks = metrics.compared
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
		RunID                         *int64         `json:"run_id"`
		Branch                        string         `json:"branch"`
		Window                        int            `json:"window"`
		ComparedRuns                  int            `json:"compared_runs"`
		MinPoints                     int            `json:"min_points"`
		EffectiveMinPoints            int            `json:"effective_min_points"`
		BaselineOffset                int            `json:"baseline_offset"`
		AlgorithmVersion              string         `json:"algorithm_version"`
		Metric                        string         `json:"metric"`
		Estimator                     string         `json:"estimator"`
		CohortPolicy                  string         `json:"cohort_policy"`
		FamilyDefinition              string         `json:"family_definition"`
		CalibrationStatus             string         `json:"calibration_status"`
		CalibrationCaveat             string         `json:"calibration_caveat"`
		FDRLevel                      float64        `json:"fdr_level"`
		HypothesisCount               int            `json:"hypothesis_count"`
		TotalBenchmarks               int            `json:"total_benchmarks"`
		AnalyzedBenchmarks            int            `json:"analyzed_benchmarks"`
		InsufficientHistory           bool           `json:"insufficient_history"`
		InsufficientReason            string         `json:"insufficient_reason,omitempty"`
		ExclusionCounts               map[string]int `json:"exclusion_counts,omitempty"`
		GlobalShiftDetected           bool           `json:"global_shift_detected"`
		GlobalShiftPositiveShare      float64        `json:"global_shift_positive_share"`
		GlobalShiftGeoIncreasePct     float64        `json:"global_shift_geo_increase_pct"`
		GlobalShiftComparedBenchmarks int            `json:"global_shift_compared_benchmarks"`
		Regressions                   []regression   `json:"regressions"`
	}

	type benchResult struct {
		testResult  stats.RegressionResult
		latest      db.Result
		baseline    *stats.BaselineStats
		changePoint *changePointDiagnostic
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
		ageByRunID := make(map[int64]int, len(benchmarkTrends))
		for age, trend := range benchmarkTrends {
			run := trend.Run
			result := trend.Result
			runDate, err := time.Parse(time.RFC3339Nano, run.RunDate)
			if err != nil {
				http.Error(w, "invalid run date: "+err.Error(), http.StatusInternalServerError)
				return
			}
			observation := stats.OrderedRunStat{RunDate: runDate, Stat: toRunStat(run.ID, result)}
			observations = append(observations, observation)
			ageByRunID[run.ID] = age
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
		var diagnostic *changePointDiagnostic
		if len(benchmarkTrends) >= 2*changePointMinSegment {
			series := make([]stats.RunStat, 0, len(benchmarkTrends))
			for i := len(benchmarkTrends) - 1; i >= 0; i-- {
				trend := benchmarkTrends[i]
				series = append(series, toRunStat(trend.Run.ID, trend.Result))
			}
			points := stats.DetectChangePoints(series, changePointMinSegment, changePointAlpha, changePointPerms)
			if len(points) > 0 {
				point := points[len(points)-1]
				diagnostic = &changePointDiagnostic{
					RunID: point.RunID, PValue: point.PValue, EffectPercent: point.EffectPercent,
					MagnitudeNs: point.Magnitude, Recent: ageByRunID[point.RunID] <= changePointMaxAgeRuns,
				}
			}
		}

		benchResults = append(benchResults, benchResult{
			testResult:  result,
			latest:      latestResult,
			baseline:    baseline,
			changePoint: diagnostic,
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
				ChangePointDiagnostic: bench.changePoint,
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
		RunID:                         &runID,
		Branch:                        branch,
		Window:                        window,
		ComparedRuns:                  len(runs),
		MinPoints:                     minPoints,
		EffectiveMinPoints:            effectiveMinPoints,
		BaselineOffset:                baselineOffset,
		AlgorithmVersion:              regressionAlgorithmVersion,
		Metric:                        regressionMetric,
		Estimator:                     regressionEstimator,
		CohortPolicy:                  regressionCohortPolicy,
		FamilyDefinition:              regressionFamilyDefinition,
		CalibrationStatus:             regressionCalibrationStatus,
		CalibrationCaveat:             regressionCalibrationCaveat,
		FDRLevel:                      defaultFDR,
		HypothesisCount:               len(benchResults),
		TotalBenchmarks:               len(benchmarkKeys),
		AnalyzedBenchmarks:            analyzableBenchmarks,
		InsufficientHistory:           analyzableBenchmarks == 0,
		ExclusionCounts:               exclusionCounts,
		GlobalShiftDetected:           globalShiftDetected,
		GlobalShiftPositiveShare:      globalShiftPositiveShare,
		GlobalShiftGeoIncreasePct:     globalShiftGeoIncreasePct,
		GlobalShiftComparedBenchmarks: globalShiftComparedBenchmarks,
		Regressions:                   regressions,
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
	branches, err := s.db.GetBranchesWithRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(branches)
}

// --- Job endpoints ---

type jobResponse struct {
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
		Branch      string `json:"branch"`
		CommitHash  string `json:"commit_hash"`
		Samples     int    `json:"samples"`
		Profile     string `json:"profile"`
		Notes       string `json:"notes"`
		RequestedBy string `json:"requested_by"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Branch == "" {
		http.Error(w, "branch is required", http.StatusBadRequest)
		return
	}

	if req.Samples <= 0 {
		req.Samples = 3
	}
	if req.Profile == "" {
		req.Profile = "cpu"
	}
	if req.Profile != "none" && req.Profile != "cpu" {
		http.Error(w, "profile must be 'none' or 'cpu'", http.StatusBadRequest)
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
		Status:      "pending",
		Kind:        "benchmark",
		Branch:      branch,
		CommitHash:  req.CommitHash,
		RepoURL:     repoURL,
		Samples:     req.Samples,
		Profile:     req.Profile,
		Notes:       req.Notes,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		RequestedBy: req.RequestedBy,
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

	jobs, err := s.db.ListJobs(limit, status, branch)
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
		CommitHash     string `json:"commit_hash"`
		CommitHashFull string `json:"commit_hash_full"`
		CommitMessage  string `json:"commit_message"`
		CommitDate     string `json:"commit_date"`
		Branch         string `json:"branch"`
		MachineID      string `json:"machine_id"`
		Notes          string `json:"notes"`
		ZigOptimize    string `json:"zig_optimize"`
		Results        []struct {
			Category             string            `json:"category"`
			Name                 string            `json:"name"`
			MinNs                int64             `json:"min_ns"`
			AvgNs                int64             `json:"avg_ns"`
			MaxNs                int64             `json:"max_ns"`
			StdDevNs             int64             `json:"std_dev_ns"`
			P50Ns                int64             `json:"p50_ns"`
			P95Ns                int64             `json:"p95_ns"`
			P99Ns                int64             `json:"p99_ns"`
			TotalNs              int64             `json:"total_ns"`
			Iterations           int64             `json:"iterations"`
			SampleCount          int64             `json:"sample_count"`
			SampleAvgVarianceNs2 *float64          `json:"sample_avg_variance_ns2"`
			SampleDataVersion    *int64            `json:"sample_data_version"`
			SummaryVersion       *int64            `json:"summary_version"`
			Samples              []db.ResultSample `json:"samples"`
			MemStats             []struct {
				Name  string `json:"name"`
				Bytes int64  `json:"bytes"`
			} `json:"mem_stats,omitempty"`
		} `json:"results"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.CommitHash == "" {
		writeJSONError(w, "commit_hash is required", http.StatusBadRequest)
		return
	}

	if req.ZigOptimize == "" {
		req.ZigOptimize = "ReleaseFast"
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

	metadata := r.URL.Query().Get("metadata")
	if metadata == "" {
		metadata = "{}"
	}

	// Read body with size limit
	body := io.LimitReader(r.Body, maxProfileSize)
	data, err := io.ReadAll(body)
	if err != nil {
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

	exists, err := s.db.HasCommit(hash)
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

	var run *db.Run
	var err error
	if branch := r.URL.Query().Get("branch"); branch != "" {
		run, err = s.db.GetLatestRunForBranch(branch)
	} else {
		run, err = s.db.GetLatestRun()
	}
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
	})
}

// handleClaimJob handles POST /api/jobs/claim - atomically claim next pending job.
func (s *Server) handleClaimJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	job, err := s.db.ClaimNextPendingJob()
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
		if err := s.db.UpdateJobCommitHash(id, *req.CommitHash); err != nil {
			writeJSONError(w, "update commit hash: "+err.Error(), http.StatusInternalServerError)
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
			if err := s.db.CompleteJob(id, *req.RunID); err != nil {
				writeJSONError(w, "complete job: "+err.Error(), http.StatusInternalServerError)
				return
			}
		case currentStatus == "running" && newStatus == "failed":
			errMsg := ""
			if req.Error != nil {
				errMsg = *req.Error
			}
			if err := s.db.FailJob(id, errMsg); err != nil {
				writeJSONError(w, "fail job: "+err.Error(), http.StatusInternalServerError)
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

// writeJSONError writes a JSON error response in the standard format.
func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
