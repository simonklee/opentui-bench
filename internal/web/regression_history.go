package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"opentui-bench/internal/db"
)

const (
	defaultRegressionHistoryLimit = 120
	maxRegressionHistoryLimit     = 500

	// Bump this when cached regression history payloads become incompatible.
	// Do not include the executable hash in cache keys: unrelated rebuilds and
	// embedded asset changes would force the landing page down the cold path.
	regressionCacheAlgorithmVersion = "v4-causal"
)

type regressionSnapshotItem struct {
	Name                   string   `json:"name"`
	Category               string   `json:"category"`
	LatestResultID         int64    `json:"latest_result_id"`
	LatestCILowerNs        int64    `json:"latest_ci_lower_ns"`
	LatestCIUpperNs        int64    `json:"latest_ci_upper_ns"`
	BaselineRunID          int64    `json:"baseline_run_id"`
	BaselineCommitHash     string   `json:"baseline_commit_hash"`
	BaselineCommitHashFull string   `json:"baseline_commit_hash_full"`
	BaselineCILowerNs      int64    `json:"baseline_ci_lower_ns"`
	BaselineCIUpperNs      int64    `json:"baseline_ci_upper_ns"`
	ChangePercent          float64  `json:"change_percent"`
	MinEffectPercent       float64  `json:"min_effect_percent"`
	PValue                 *float64 `json:"p_value,omitempty"`
	AdjustedPValue         *float64 `json:"adjusted_p_value,omitempty"`
	DetectionMethod        string   `json:"detection_method"`
	Alpha                  float64  `json:"alpha"`
}

type regressionSnapshot struct {
	RunID                         *int64                   `json:"run_id"`
	Branch                        string                   `json:"branch"`
	Window                        int                      `json:"window"`
	ComparedRuns                  int                      `json:"compared_runs"`
	MinPoints                     int                      `json:"min_points"`
	EffectiveMinPoints            int                      `json:"effective_min_points"`
	BaselineOffset                int                      `json:"baseline_offset"`
	DFMode                        string                   `json:"df_mode"`
	TotalBenchmarks               int                      `json:"total_benchmarks"`
	AnalyzedBenchmarks            int                      `json:"analyzed_benchmarks"`
	InsufficientHistory           bool                     `json:"insufficient_history"`
	InsufficientReason            string                   `json:"insufficient_reason,omitempty"`
	ExclusionCounts               map[string]int           `json:"exclusion_counts,omitempty"`
	GlobalShiftDetected           bool                     `json:"global_shift_detected"`
	GlobalShiftPositiveShare      float64                  `json:"global_shift_positive_share"`
	GlobalShiftGeoIncreasePct     float64                  `json:"global_shift_geo_increase_pct"`
	GlobalShiftComparedBenchmarks int                      `json:"global_shift_compared_benchmarks"`
	Regressions                   []regressionSnapshotItem `json:"regressions"`
}

type regressionHistoryEntry struct {
	RunID                         int64                    `json:"run_id"`
	CommitHash                    string                   `json:"commit_hash"`
	CommitHashFull                string                   `json:"commit_hash_full"`
	CommitMessage                 string                   `json:"commit_message"`
	RunDate                       string                   `json:"run_date"`
	Branch                        string                   `json:"branch"`
	Cached                        bool                     `json:"cached"`
	CachedAt                      string                   `json:"cached_at,omitempty"`
	RegressionCount               int                      `json:"regression_count"`
	ComparedRuns                  int                      `json:"compared_runs"`
	MinPoints                     int                      `json:"min_points"`
	EffectiveMinPoints            int                      `json:"effective_min_points"`
	BaselineOffset                int                      `json:"baseline_offset"`
	TotalBenchmarks               int                      `json:"total_benchmarks"`
	AnalyzedBenchmarks            int                      `json:"analyzed_benchmarks"`
	InsufficientHistory           bool                     `json:"insufficient_history"`
	InsufficientReason            string                   `json:"insufficient_reason,omitempty"`
	GlobalShiftDetected           bool                     `json:"global_shift_detected"`
	GlobalShiftPositiveShare      float64                  `json:"global_shift_positive_share"`
	GlobalShiftGeoIncreasePct     float64                  `json:"global_shift_geo_increase_pct"`
	GlobalShiftComparedBenchmarks int                      `json:"global_shift_compared_benchmarks"`
	Regressions                   []regressionSnapshotItem `json:"regressions"`
}

type regressionHistoryResponse struct {
	Branch         string                   `json:"branch"`
	Window         int                      `json:"window"`
	MinPoints      int                      `json:"min_points"`
	BaselineOffset int                      `json:"baseline_offset"`
	DFMode         string                   `json:"df_mode"`
	GenerationKey  string                   `json:"generation_key"`
	ScannedRuns    int                      `json:"scanned_runs"`
	EntryCount     int                      `json:"entry_count"`
	CachedRuns     int                      `json:"cached_runs"`
	ComputedRuns   int                      `json:"computed_runs"`
	Entries        []regressionHistoryEntry `json:"entries"`
}

func (s *Server) handleRegressionsHistory(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("method"); raw != "" {
		http.Error(w, "method parameter is no longer supported; use df_mode only", http.StatusBadRequest)
		return
	}

	dfMode := defaultRegressionDFMode
	if raw := r.URL.Query().Get("df_mode"); raw != "" {
		switch raw {
		case regressionDFModeBaseline, regressionDFModeLatest:
			dfMode = raw
		default:
			http.Error(w, "invalid df_mode, expected 'baseline' or 'latest'", http.StatusBadRequest)
			return
		}
	}

	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}

	window := defaultWindow
	if raw := r.URL.Query().Get("window"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			window = n
		}
	}

	minPoints := defaultMinPoints
	if raw := r.URL.Query().Get("min_points"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			minPoints = n
		}
	}

	baselineOffset := defaultBaselineOffset
	if raw := r.URL.Query().Get("baseline_offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			baselineOffset = n
		}
	}

	limit := defaultRegressionHistoryLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxRegressionHistoryLimit {
		limit = maxRegressionHistoryLimit
	}

	runs, err := s.db.ListRunsForBranch(branch, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	generationKey := s.regressionCacheGenerationKey(branch, window, minPoints, baselineOffset, dfMode)
	now := time.Now().UTC().Format(time.RFC3339)

	entries := make([]regressionHistoryEntry, 0, len(runs))
	cachedRuns := 0
	computedRuns := 0

	for _, run := range runs {
		cacheKey := db.RegressionCacheKey{
			RunID:          run.ID,
			Branch:         branch,
			Window:         window,
			MinPoints:      minPoints,
			BaselineOffset: baselineOffset,
			DFMode:         dfMode,
		}

		cacheEntry, err := s.db.GetRegressionCache(cacheKey, generationKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		payload := ""
		cachedAt := ""
		fromCache := false
		if cacheEntry != nil {
			fromCache = true
			cachedRuns++
			payload = cacheEntry.ResponseJSON
			cachedAt = cacheEntry.UpdatedAt
		} else {
			snapshotJSON, err := s.computeRegressionsSnapshot(r.Context(), run.ID, branch, window, minPoints, baselineOffset, dfMode)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			payload = string(snapshotJSON)
			if err := s.db.UpsertRegressionCache(&db.RegressionCacheEntry{
				Key:           cacheKey,
				GenerationKey: generationKey,
				ResponseJSON:  payload,
			}); err != nil {
				fmt.Printf("warning: store regression cache for run %d: %v\n", run.ID, err)
			} else {
				cachedAt = now
			}

			computedRuns++
		}

		var snapshot regressionSnapshot
		if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
			http.Error(w, fmt.Sprintf("decode regression snapshot for run %d: %v", run.ID, err), http.StatusInternalServerError)
			return
		}

		regressions := snapshot.Regressions
		if regressions == nil {
			regressions = []regressionSnapshotItem{}
		}

		if len(regressions) == 0 && !snapshot.GlobalShiftDetected {
			continue
		}

		runBranch := run.Branch
		if runBranch == "" {
			runBranch = "main"
		}

		entries = append(entries, regressionHistoryEntry{
			RunID:                         run.ID,
			CommitHash:                    run.CommitHash,
			CommitHashFull:                run.CommitHashFull,
			CommitMessage:                 run.CommitMessage,
			RunDate:                       run.RunDate,
			Branch:                        runBranch,
			Cached:                        fromCache,
			CachedAt:                      cachedAt,
			RegressionCount:               len(regressions),
			ComparedRuns:                  snapshot.ComparedRuns,
			MinPoints:                     snapshot.MinPoints,
			EffectiveMinPoints:            snapshot.EffectiveMinPoints,
			BaselineOffset:                snapshot.BaselineOffset,
			TotalBenchmarks:               snapshot.TotalBenchmarks,
			AnalyzedBenchmarks:            snapshot.AnalyzedBenchmarks,
			InsufficientHistory:           snapshot.InsufficientHistory,
			InsufficientReason:            snapshot.InsufficientReason,
			GlobalShiftDetected:           snapshot.GlobalShiftDetected,
			GlobalShiftPositiveShare:      snapshot.GlobalShiftPositiveShare,
			GlobalShiftGeoIncreasePct:     snapshot.GlobalShiftGeoIncreasePct,
			GlobalShiftComparedBenchmarks: snapshot.GlobalShiftComparedBenchmarks,
			Regressions:                   regressions,
		})
	}

	response := regressionHistoryResponse{
		Branch:         branch,
		Window:         window,
		MinPoints:      minPoints,
		BaselineOffset: baselineOffset,
		DFMode:         dfMode,
		GenerationKey:  generationKey,
		ScannedRuns:    len(runs),
		EntryCount:     len(entries),
		CachedRuns:     cachedRuns,
		ComputedRuns:   computedRuns,
		Entries:        entries,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) computeRegressionsSnapshot(ctx context.Context, runID int64, branch string, window int, minPoints int, baselineOffset int, dfMode string) ([]byte, error) {
	query := url.Values{}
	query.Set("run_id", strconv.FormatInt(runID, 10))
	query.Set("branch", branch)
	query.Set("window", strconv.Itoa(window))
	query.Set("min_points", strconv.Itoa(minPoints))
	query.Set("baseline_offset", strconv.Itoa(baselineOffset))
	query.Set("df_mode", dfMode)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/regressions?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build regressions request for run %d: %w", runID, err)
	}

	recorder := newInMemoryResponseWriter()
	s.handleRegressions(recorder, req)
	if recorder.statusCode != http.StatusOK {
		return nil, fmt.Errorf("compute regressions for run %d: status=%d body=%q", runID, recorder.statusCode, strings.TrimSpace(recorder.body.String()))
	}

	payload := bytes.TrimSpace(recorder.body.Bytes())
	if len(payload) == 0 {
		return nil, fmt.Errorf("compute regressions for run %d: empty response", runID)
	}

	return payload, nil
}

func (s *Server) regressionCacheGenerationKey(branch string, window int, minPoints int, baselineOffset int, dfMode string) string {
	cacheSeed := fmt.Sprintf(
		"algorithm=%s|branch=%s|window=%d|min_points=%d|baseline_offset=%d|df_mode=%s|alpha=%g|fdr=%g|min_abs_ns=%g|global_shift=%d,%g,%g|change_point=%d,%g,%d,%d|default_df_mode=%s",
		regressionCacheAlgorithmVersion,
		branch,
		window,
		minPoints,
		baselineOffset,
		dfMode,
		defaultAlpha,
		defaultFDR,
		defaultMinAbsoluteNs,
		globalShiftMinBenchmarks,
		globalShiftMinPositiveShare,
		globalShiftMinGeoIncreasePct,
		changePointMinSegment,
		changePointAlpha,
		changePointPerms,
		changePointMaxAgeRuns,
		defaultRegressionDFMode,
	)

	sum := sha256.Sum256([]byte(cacheSeed))
	return hex.EncodeToString(sum[:])
}

type inMemoryResponseWriter struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func newInMemoryResponseWriter() *inMemoryResponseWriter {
	return &inMemoryResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (w *inMemoryResponseWriter) Header() http.Header {
	return w.header
}

func (w *inMemoryResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *inMemoryResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}
