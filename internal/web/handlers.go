package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
)

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
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
		ID          int64             `json:"id"`
		Category    string            `json:"category"`
		Name        string            `json:"name"`
		MinNs       int64             `json:"min_ns"`
		AvgNs       int64             `json:"avg_ns"`
		MaxNs       int64             `json:"max_ns"`
		StdDevNs    int64             `json:"std_dev_ns"`
		P50Ns       int64             `json:"p50_ns"`
		P95Ns       int64             `json:"p95_ns"`
		P99Ns       int64             `json:"p99_ns"`
		Iterations  int64             `json:"iterations"`
		SampleCount int64             `json:"sample_count"`
		MemStats    []memStatResponse `json:"mem_stats,omitempty"`
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
			ID:          res.ID,
			Category:    res.Category,
			Name:        res.Name,
			MinNs:       res.MinNs,
			AvgNs:       res.AvgNs,
			MaxNs:       res.MaxNs,
			StdDevNs:    res.StdDevNs,
			P50Ns:       res.P50Ns,
			P95Ns:       res.P95Ns,
			P99Ns:       res.P99Ns,
			Iterations:  res.Iterations,
			SampleCount: res.SampleCount,
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

	type resultKey struct {
		Category string
		Name     string
	}
	resultsBMap := make(map[resultKey]int64)
	for _, r := range resultsB {
		resultsBMap[resultKey{Category: r.Category, Name: r.Name}] = r.AvgNs
	}

	type comparison struct {
		Name          string  `json:"name"`
		Category      string  `json:"category"`
		BaselineNs    int64   `json:"baseline_ns"`
		CurrentNs     int64   `json:"current_ns"`
		ChangePercent float64 `json:"change_percent"`
		IsRegression  bool    `json:"is_regression"`
	}

	var comparisons []comparison
	threshold := 10.0

	for _, rA := range resultsA {
		if avgB, ok := resultsBMap[resultKey{Category: rA.Category, Name: rA.Name}]; ok {
			var change float64
			if rA.AvgNs != 0 {
				change = float64(avgB-rA.AvgNs) / float64(rA.AvgNs) * 100
			}
			comparisons = append(comparisons, comparison{
				Name:          rA.Name,
				Category:      rA.Category,
				BaselineNs:    rA.AvgNs,
				CurrentNs:     avgB,
				ChangePercent: change,
				IsRegression:  change > threshold,
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
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name parameter required", http.StatusBadRequest)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}

	trends, err := s.db.GetTrend(name, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type trendPoint struct {
		RunID       int64  `json:"run_id"`
		ResultID    int64  `json:"result_id"`
		CommitHash  string `json:"commit_hash"`
		RunDate     string `json:"run_date"`
		AvgNs       int64  `json:"avg_ns"`
		MinNs       int64  `json:"min_ns"`
		MaxNs       int64  `json:"max_ns"`
		StdDevNs    int64  `json:"std_dev_ns"`
		SampleCount int64  `json:"sample_count"`
	}

	var points []trendPoint
	for _, t := range trends {
		points = append(points, trendPoint{
			RunID:       t.Run.ID,
			ResultID:    t.Result.ID,
			CommitHash:  t.Run.CommitHash,
			RunDate:     t.Run.RunDate,
			AvgNs:       t.Result.AvgNs,
			MinNs:       t.Result.MinNs,
			MaxNs:       t.Result.MaxNs,
			StdDevNs:    t.Result.StdDevNs,
			SampleCount: t.Result.SampleCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}

func (s *Server) handleBenchmarks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT DISTINCT name FROM results ORDER BY name`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(names); err != nil {
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
	defer rows.Close()

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
	defer os.Remove(tmp.Name())

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

	cacheKey := result.Name
	legacyCacheKey := fmt.Sprintf("%d", result.ID)
	if s.svgCache != nil {
		if svg, ok := s.svgCache.Get(runID, cacheKey); ok {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write(svg)
			return
		}
		if legacyCacheKey != cacheKey {
			if svg, ok := s.svgCache.Get(runID, legacyCacheKey); ok {
				_ = s.svgCache.Put(runID, cacheKey, svg)
				w.Header().Set("Content-Type", "image/svg+xml")
				w.Write(svg)
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
		w.Write(cached.DataBlob)
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

	if fg, err := s.db.GetFlamegraph(runID, result.Name); err == nil {
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
		w.Write(svg)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
	w.Write(svg)
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

	cacheKey := "callgraph:" + result.Name
	legacyCacheKey := fmt.Sprintf("callgraph:%d", result.ID)
	if s.svgCache != nil {
		if svg, ok := s.svgCache.Get(runID, cacheKey); ok {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write(svg)
			return
		}
		if legacyCacheKey != cacheKey {
			if svg, ok := s.svgCache.Get(runID, legacyCacheKey); ok {
				_ = s.svgCache.Put(runID, cacheKey, svg)
				w.Header().Set("Content-Type", "image/svg+xml")
				w.Write(svg)
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
		w.Write(cached.DataBlob)
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
	w.Write(svg)
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
	filename := fmt.Sprintf("%s_%d_%d.%s", sanitizedName, runID, resultID, kind)
	filename = filepath.Base(filepath.Clean(filename))

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Write(artifact.DataBlob)
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
