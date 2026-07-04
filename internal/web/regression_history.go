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

	"opentui-bench/internal/broadshift"
	"opentui-bench/internal/db"
	"opentui-bench/internal/stats"
)

const (
	defaultRegressionHistoryLimit = 120
	maxRegressionHistoryLimit     = 500

	// Bump this when cached regression history payloads become incompatible.
	// Do not include the executable hash in cache keys: unrelated rebuilds and
	// embedded asset changes would force the landing page down the cold path.
	regressionCacheAlgorithmVersion = regressionAlgorithmVersion
)

type regressionSnapshotItem struct {
	Name                   string                           `json:"name"`
	Category               string                           `json:"category"`
	LatestResultID         int64                            `json:"latest_result_id"`
	LatestCILowerNs        int64                            `json:"latest_ci_lower_ns"`
	LatestCIUpperNs        int64                            `json:"latest_ci_upper_ns"`
	BaselineRunID          int64                            `json:"baseline_run_id"`
	BaselineCommitHash     string                           `json:"baseline_commit_hash"`
	BaselineCommitHashFull string                           `json:"baseline_commit_hash_full"`
	BaselineCILowerNs      int64                            `json:"baseline_ci_lower_ns"`
	BaselineCIUpperNs      int64                            `json:"baseline_ci_upper_ns"`
	ChangePercent          float64                          `json:"change_percent"`
	AbsoluteChangeNs       float64                          `json:"absolute_change_ns"`
	BaselineNs             float64                          `json:"baseline_ns"`
	MinEffectPercent       float64                          `json:"min_effect_percent"`
	PValue                 *float64                         `json:"p_value,omitempty"`
	AdjustedPValue         *float64                         `json:"adjusted_p_value,omitempty"`
	DetectionMethod        string                           `json:"detection_method"`
	TScore                 *float64                         `json:"t_score,omitempty"`
	DegreesOfFreedom       int                              `json:"degrees_of_freedom"`
	ChangePointDiagnostic  *regressionChangePointDiagnostic `json:"change_point_diagnostic,omitempty"`
}

type regressionChangePointDiagnostic struct {
	RunID         int64   `json:"run_id"`
	PValue        float64 `json:"p_value"`
	EffectPercent float64 `json:"effect_percent"`
	MagnitudeNs   float64 `json:"magnitude_ns"`
	Recent        bool    `json:"recent"`
}

type regressionSnapshot struct {
	RunID               *int64                   `json:"run_id"`
	Branch              string                   `json:"branch"`
	Window              int                      `json:"window"`
	ComparedRuns        int                      `json:"compared_runs"`
	MinPoints           int                      `json:"min_points"`
	EffectiveMinPoints  int                      `json:"effective_min_points"`
	BaselineOffset      int                      `json:"baseline_offset"`
	AlgorithmVersion    string                   `json:"algorithm_version"`
	Metric              string                   `json:"metric"`
	Estimator           string                   `json:"estimator"`
	CohortPolicy        string                   `json:"cohort_policy"`
	FamilyDefinition    string                   `json:"family_definition"`
	CalibrationStatus   string                   `json:"calibration_status"`
	CalibrationCaveat   string                   `json:"calibration_caveat"`
	FDRLevel            float64                  `json:"fdr_level"`
	HypothesisCount     int                      `json:"hypothesis_count"`
	TotalBenchmarks     int                      `json:"total_benchmarks"`
	AnalyzedBenchmarks  int                      `json:"analyzed_benchmarks"`
	InsufficientHistory bool                     `json:"insufficient_history"`
	InsufficientReason  string                   `json:"insufficient_reason,omitempty"`
	ExclusionCounts     map[string]int           `json:"exclusion_counts,omitempty"`
	BroadShift          broadshift.Incident      `json:"broad_shift"`
	Regressions         []regressionSnapshotItem `json:"regressions"`
}

type regressionHistoryEntry struct {
	RunID               int64                    `json:"run_id"`
	CommitHash          string                   `json:"commit_hash"`
	CommitHashFull      string                   `json:"commit_hash_full"`
	CommitMessage       string                   `json:"commit_message"`
	RunDate             string                   `json:"run_date"`
	Branch              string                   `json:"branch"`
	Cached              bool                     `json:"cached"`
	CachedAt            string                   `json:"cached_at,omitempty"`
	RegressionCount     int                      `json:"regression_count"`
	ComparedRuns        int                      `json:"compared_runs"`
	MinPoints           int                      `json:"min_points"`
	EffectiveMinPoints  int                      `json:"effective_min_points"`
	BaselineOffset      int                      `json:"baseline_offset"`
	TotalBenchmarks     int                      `json:"total_benchmarks"`
	AnalyzedBenchmarks  int                      `json:"analyzed_benchmarks"`
	InsufficientHistory bool                     `json:"insufficient_history"`
	InsufficientReason  string                   `json:"insufficient_reason,omitempty"`
	BroadShift          broadshift.Incident      `json:"broad_shift"`
	Regressions         []regressionSnapshotItem `json:"regressions"`
}

type regressionHistoryResponse struct {
	Branch            string                   `json:"branch"`
	Window            int                      `json:"window"`
	MinPoints         int                      `json:"min_points"`
	BaselineOffset    int                      `json:"baseline_offset"`
	AlgorithmVersion  string                   `json:"algorithm_version"`
	Metric            string                   `json:"metric"`
	Estimator         string                   `json:"estimator"`
	CohortPolicy      string                   `json:"cohort_policy"`
	FamilyDefinition  string                   `json:"family_definition"`
	CalibrationStatus string                   `json:"calibration_status"`
	CalibrationCaveat string                   `json:"calibration_caveat"`
	FDRLevel          float64                  `json:"fdr_level"`
	GenerationKey     string                   `json:"generation_key"`
	ScannedRuns       int                      `json:"scanned_runs"`
	EntryCount        int                      `json:"entry_count"`
	CachedRuns        int                      `json:"cached_runs"`
	ComputedRuns      int                      `json:"computed_runs"`
	Entries           []regressionHistoryEntry `json:"entries"`
}

func (s *Server) handleRegressionsHistory(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("method"); raw != "" {
		http.Error(w, "method parameter is no longer supported", http.StatusBadRequest)
		return
	}
	if raw := r.URL.Query().Get("df_mode"); raw != "" {
		http.Error(w, "df_mode is no longer supported; degrees of freedom are always history_count - 1", http.StatusBadRequest)
		return
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

	generationKey := s.regressionCacheGenerationKey(branch, window, minPoints, baselineOffset, "target-specific")
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
		}

		dataFingerprint, err := s.db.RegressionDataFingerprint(run.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		targetGenerationKey := s.regressionCacheGenerationKey(branch, window, minPoints, baselineOffset, dataFingerprint)
		cacheEntry, err := s.db.GetRegressionCache(cacheKey, targetGenerationKey)
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
			var snapshotJSON []byte
			stable := false
			for attempt := 0; attempt < 2; attempt++ {
				snapshotJSON, err = s.computeRegressionsSnapshot(r.Context(), run.ID, branch, window, minPoints, baselineOffset)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				currentFingerprint, err := s.db.RegressionDataFingerprint(run.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if currentFingerprint == dataFingerprint {
					stable = true
					break
				}
				dataFingerprint = currentFingerprint
				targetGenerationKey = s.regressionCacheGenerationKey(branch, window, minPoints, baselineOffset, dataFingerprint)
			}
			if !stable {
				http.Error(w, fmt.Sprintf("regression inputs changed while computing run %d", run.ID), http.StatusConflict)
				return
			}

			payload = string(snapshotJSON)
			if err := s.db.UpsertRegressionCache(&db.RegressionCacheEntry{
				Key:           cacheKey,
				GenerationKey: targetGenerationKey,
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

		if len(regressions) == 0 && !snapshot.BroadShift.Detected {
			continue
		}

		runBranch := run.Branch
		if runBranch == "" {
			runBranch = "main"
		}

		entries = append(entries, regressionHistoryEntry{
			RunID:               run.ID,
			CommitHash:          run.CommitHash,
			CommitHashFull:      run.CommitHashFull,
			CommitMessage:       run.CommitMessage,
			RunDate:             run.RunDate,
			Branch:              runBranch,
			Cached:              fromCache,
			CachedAt:            cachedAt,
			RegressionCount:     len(regressions),
			ComparedRuns:        snapshot.ComparedRuns,
			MinPoints:           snapshot.MinPoints,
			EffectiveMinPoints:  snapshot.EffectiveMinPoints,
			BaselineOffset:      snapshot.BaselineOffset,
			TotalBenchmarks:     snapshot.TotalBenchmarks,
			AnalyzedBenchmarks:  snapshot.AnalyzedBenchmarks,
			InsufficientHistory: snapshot.InsufficientHistory,
			InsufficientReason:  snapshot.InsufficientReason,
			BroadShift:          snapshot.BroadShift,
			Regressions:         regressions,
		})
	}

	response := regressionHistoryResponse{
		Branch:            branch,
		Window:            window,
		MinPoints:         minPoints,
		BaselineOffset:    baselineOffset,
		AlgorithmVersion:  regressionAlgorithmVersion,
		Metric:            regressionMetric,
		Estimator:         regressionEstimator,
		CohortPolicy:      regressionCohortPolicy,
		FamilyDefinition:  regressionFamilyDefinition,
		CalibrationStatus: regressionCalibrationStatus,
		CalibrationCaveat: regressionCalibrationCaveat,
		FDRLevel:          defaultFDR,
		GenerationKey:     generationKey,
		ScannedRuns:       len(runs),
		EntryCount:        len(entries),
		CachedRuns:        cachedRuns,
		ComputedRuns:      computedRuns,
		Entries:           entries,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) computeRegressionsSnapshot(ctx context.Context, runID int64, branch string, window int, minPoints int, baselineOffset int) ([]byte, error) {
	query := url.Values{}
	query.Set("run_id", strconv.FormatInt(runID, 10))
	query.Set("branch", branch)
	query.Set("window", strconv.Itoa(window))
	query.Set("min_points", strconv.Itoa(minPoints))
	query.Set("baseline_offset", strconv.Itoa(baselineOffset))

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

func (s *Server) regressionCacheGenerationKey(branch string, window int, minPoints int, baselineOffset int, dataFingerprint string) string {
	cacheSeed := fmt.Sprintf(
		"algorithm=%s|metric=%s|estimator=%s|cohort=%s|family=%s|schema=%d|sample_data=%d|summary=%d|branch=%s|window=%d|min_points=%d|baseline_offset=%d|data=%s|fdr=%g|min_relative_pct=%g|min_abs_ns=%g|broad_shift=%d,%g,%g|change_point_diagnostic=%d,%g,%d,%d",
		regressionCacheAlgorithmVersion,
		regressionMetric,
		regressionEstimator,
		regressionCohortPolicy,
		regressionFamilyDefinition,
		db.CurrentSchemaVersion,
		db.CurrentSampleDataVersion,
		db.CurrentSummaryVersion,
		branch,
		window,
		minPoints,
		baselineOffset,
		dataFingerprint,
		defaultFDR,
		stats.MinPracticalRegressionEffectPercent,
		defaultMinAbsoluteNs,
		broadShiftMinBenchmarks,
		broadShiftMinPositiveShare,
		broadShiftMinGeoIncreasePct,
		changePointMinSegment,
		changePointAlpha,
		changePointPerms,
		changePointMaxAgeRuns,
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
