package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opentui-bench/internal/broadshift"
	"opentui-bench/internal/db"
)

func openRegressionHistoryTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func insertRegressionHistoryTestRun(t *testing.T, database *db.DB, branch string, at time.Time, suffix string) int64 {
	t.Helper()
	runID, err := database.InsertRun(&db.Run{
		CommitHash:     fmt.Sprintf("%s-%s", branch, suffix),
		CommitHashFull: fmt.Sprintf("%s-%s", branch, suffix),
		CommitMessage:  "test",
		CommitDate:     at.Format(time.RFC3339),
		Branch:         branch,
		RunDate:        at.Format(time.RFC3339),
		MachineID:      "runner",
		ZigOptimize:    "ReleaseFast",
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return runID
}

func insertRegressionHistoryTestResult(t *testing.T, database *db.DB, runID int64, median int64) {
	t.Helper()
	_, err := database.InsertResult(&db.Result{
		RunID: runID, Category: "render", Name: "frame", MinNs: median - 10,
		AvgNs: median, MaxNs: median + 10, StdDevNs: 10, P50Ns: median,
		P95Ns: median + 5, P99Ns: median + 9, TotalNs: median * 100,
		Iterations: 100, SampleCount: 3,
	})
	if err != nil {
		t.Fatalf("insert result: %v", err)
	}
}

func TestRegressionCacheGenerationKeyIsTargetIndependent(t *testing.T) {
	server := &Server{}

	before := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, "same-data")
	insertRegressionHistoryTestRun(t, openRegressionHistoryTestDB(t), "main", time.Now(), "future")
	after := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, "same-data")
	if before != after {
		t.Fatalf("future data changed generation key: %q != %q", before, after)
	}
}

func TestRegressionHistoryCacheIgnoresFutureMainRuns(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(i)*time.Hour), fmt.Sprintf("main-%d", i))
		insertRegressionHistoryTestResult(t, database, runID, 100_000)
	}
	featureID := insertRegressionHistoryTestRun(t, database, "feature/cache", at.Add(6*time.Hour), "feature")
	insertRegressionHistoryTestResult(t, database, featureID, 200_000)

	requestHistory := func() regressionHistoryResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/regressions/history?branch=feature%2Fcache&limit=1&min_points=5&baseline_offset=0", nil)
		rec := httptest.NewRecorder()
		server.handleRegressionsHistory(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
		}
		var response regressionHistoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response
	}

	first := requestHistory()
	if first.ComputedRuns != 1 || first.CachedRuns != 0 {
		t.Fatalf("first request computed=%d cached=%d, want 1/0", first.ComputedRuns, first.CachedRuns)
	}
	futureID := insertRegressionHistoryTestRun(t, database, "main", at.Add(7*time.Hour), "future")
	insertRegressionHistoryTestResult(t, database, futureID, 900_000)

	second := requestHistory()
	if second.ComputedRuns != 0 || second.CachedRuns != 1 {
		t.Fatalf("after future run computed=%d cached=%d, want 0/1", second.ComputedRuns, second.CachedRuns)
	}
}

func TestRegressionHistoryBatchFingerprintPreservesWarmCacheBehavior(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(i)*time.Hour), fmt.Sprintf("run-%d", i))
		insertRegressionHistoryTestResult(t, database, runID, 100_000+int64(i)*100_000)
	}

	requestHistory := func() regressionHistoryResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/regressions/history?branch=main&limit=3&min_points=2&baseline_offset=0", nil)
		rec := httptest.NewRecorder()
		server.handleRegressionsHistory(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
		}
		var response regressionHistoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response
	}

	first := requestHistory()
	second := requestHistory()
	if first.ComputedRuns != 3 || first.CachedRuns != 0 {
		t.Fatalf("cold history computed=%d cached=%d, want 3/0", first.ComputedRuns, first.CachedRuns)
	}
	if second.ComputedRuns != 0 || second.CachedRuns != 3 {
		t.Fatalf("warm history computed=%d cached=%d, want 0/3", second.ComputedRuns, second.CachedRuns)
	}
	if second.ScannedRuns != first.ScannedRuns || second.EntryCount != first.EntryCount {
		t.Fatalf("warm history changed behavior: first=%+v second=%+v", first, second)
	}
}

func TestRegressionHistoryBoundsColdComputations(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	totalRuns := maxRegressionHistoryComputations + 2
	for i := 0; i < totalRuns; i++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(i)*time.Hour), fmt.Sprintf("run-%d", i))
		insertRegressionHistoryTestResult(t, database, runID, 100_000)
	}

	requestHistory := func() regressionHistoryResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions/history?branch=main&limit=%d&min_points=2&baseline_offset=0", totalRuns), nil)
		rec := httptest.NewRecorder()
		server.handleRegressionsHistory(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
		}
		var response regressionHistoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := requestHistory()
	if first.ComputedRuns != maxRegressionHistoryComputations || first.RemainingRuns != 2 || first.Complete {
		t.Fatalf("first response progress = computed %d, remaining %d, complete %t", first.ComputedRuns, first.RemainingRuns, first.Complete)
	}
	second := requestHistory()
	if second.ComputedRuns != 2 || second.CachedRuns != maxRegressionHistoryComputations || second.RemainingRuns != 0 || !second.Complete {
		t.Fatalf("second response progress = computed %d, cached %d, remaining %d, complete %t", second.ComputedRuns, second.CachedRuns, second.RemainingRuns, second.Complete)
	}
}

func TestRegressionsBackfillsSparseBenchmarkHistory(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 6; i++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(i)*time.Hour), fmt.Sprintf("prior-%d", i))
		if i < 5 {
			insertRegressionHistoryTestResult(t, database, runID, 100_000)
		}
	}
	targetID := insertRegressionHistoryTestRun(t, database, "main", at.Add(6*time.Hour), "target")
	insertRegressionHistoryTestResult(t, database, targetID, 100_000)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d&window=5&min_points=5&baseline_offset=0", targetID), nil)
	rec := httptest.NewRecorder()
	server.handleRegressions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}

	var response struct {
		AnalyzedBenchmarks int `json:"analyzed_benchmarks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AnalyzedBenchmarks != 1 {
		t.Fatalf("analyzed_benchmarks = %d, want 1; body=%s", response.AnalyzedBenchmarks, rec.Body.String())
	}
}

func TestRegressionsUsesCompleteFamilyThenFixedGates(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	insertResult := func(runID int64, name string, avg int64) {
		t.Helper()
		_, err := database.InsertResult(&db.Result{
			RunID: runID, Category: "render", Name: name,
			MinNs: avg, AvgNs: avg, MaxNs: avg, StdDevNs: 0,
			P50Ns: avg, P95Ns: avg, P99Ns: avg, TotalNs: avg,
			Iterations: 1, SampleCount: 1,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	for i := 0; i < 5; i++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(i)*time.Hour), fmt.Sprintf("prior-%d", i))
		for _, name := range []string{"large", "improvement", "below-floor"} {
			insertResult(runID, name, 100_000)
		}
	}
	targetID := insertRegressionHistoryTestRun(t, database, "main", at.Add(5*time.Hour), "target")
	insertResult(targetID, "large", 200_000)
	insertResult(targetID, "improvement", 50_000)
	insertResult(targetID, "below-floor", 102_000)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d&window=5&min_points=5&baseline_offset=0", targetID), nil)
	rec := httptest.NewRecorder()
	server.handleRegressions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	var response struct {
		AlgorithmVersion  string                   `json:"algorithm_version"`
		Metric            string                   `json:"metric"`
		CalibrationStatus string                   `json:"calibration_status"`
		HypothesisCount   int                      `json:"hypothesis_count"`
		Regressions       []regressionSnapshotItem `json:"regressions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.HypothesisCount != 3 {
		t.Fatalf("hypothesis_count = %d, want complete family of 3", response.HypothesisCount)
	}
	if len(response.Regressions) != 1 || response.Regressions[0].Name != "large" {
		t.Fatalf("regressions = %#v, want only post-BH over-gate alert", response.Regressions)
	}
	if response.AlgorithmVersion != regressionAlgorithmVersion || response.Metric != regressionMetric || response.CalibrationStatus != regressionCalibrationStatus {
		t.Fatalf("missing score metadata: %#v", response)
	}
}

func TestBroadShiftPreservesAlertsAndHistoryCacheMetadata(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	insertResult := func(runID int64, category string, name string, avg int64) {
		t.Helper()
		_, err := database.InsertResult(&db.Result{
			RunID: runID, Category: category, Name: name,
			MinNs: avg, AvgNs: avg, MaxNs: avg, P50Ns: avg, P95Ns: avg, P99Ns: avg,
			TotalNs: avg, Iterations: 1, SampleCount: 1,
		})
		if err != nil {
			t.Fatalf("insert result: %v", err)
		}
	}

	for historyIndex := 0; historyIndex < 8; historyIndex++ {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(historyIndex)*time.Hour), fmt.Sprintf("prior-%d", historyIndex))
		for benchmarkIndex := 0; benchmarkIndex < broadShiftMinBenchmarks; benchmarkIndex++ {
			insertResult(runID, "render", fmt.Sprintf("bench-%02d", benchmarkIndex), 100_000+int64(historyIndex*100))
		}
	}
	targetID := insertRegressionHistoryTestRun(t, database, "main", at.Add(8*time.Hour), "target")
	for benchmarkIndex := 0; benchmarkIndex < broadShiftMinBenchmarks; benchmarkIndex++ {
		insertResult(targetID, "render", fmt.Sprintf("bench-%02d", benchmarkIndex), 130_000)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d&window=5&min_points=5&baseline_offset=3", targetID), nil)
	rec := httptest.NewRecorder()
	server.handleRegressions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	var snapshot regressionSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.BroadShift.Detected || snapshot.BroadShift.Cause != broadshift.CauseUnclassified || snapshot.BroadShift.Meaning != broadshift.Meaning {
		t.Fatalf("broad shift = %+v", snapshot.BroadShift)
	}
	if snapshot.BroadShift.ComparedBenchmarks != broadShiftMinBenchmarks || len(snapshot.Regressions) != broadShiftMinBenchmarks {
		t.Fatalf("compared=%d regressions=%d, want all %d alerts retained", snapshot.BroadShift.ComparedBenchmarks, len(snapshot.Regressions), broadShiftMinBenchmarks)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &shape); err != nil {
		t.Fatal(err)
	}
	if _, ok := shape["broad_shift"]; !ok {
		t.Fatal("response is missing structured broad_shift metadata")
	}
	if _, ok := shape["global_shift_detected"]; ok {
		t.Fatal("legacy flat global-shift fields must not remain")
	}
	if snapshot.EffectiveMinPoints != 5 || snapshot.BaselineOffset != 3 || snapshot.Regressions[0].DegreesOfFreedom != 4 {
		t.Fatalf("baseline changed during broad shift: min=%d offset=%d df=%d", snapshot.EffectiveMinPoints, snapshot.BaselineOffset, snapshot.Regressions[0].DegreesOfFreedom)
	}

	requestHistory := func() regressionHistoryResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/regressions/history?branch=main&limit=1&window=5&min_points=5&baseline_offset=3", nil)
		rec := httptest.NewRecorder()
		server.handleRegressionsHistory(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("history status = %d; body=%q", rec.Code, rec.Body.String())
		}
		var response regressionHistoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := requestHistory()
	second := requestHistory()
	if len(first.Entries) != 1 || !first.Entries[0].BroadShift.Detected || len(first.Entries[0].Regressions) != broadShiftMinBenchmarks {
		t.Fatalf("history lost incident or members: %+v", first.Entries)
	}
	if second.CachedRuns != 1 || second.ComputedRuns != 0 || second.Entries[0].BroadShift.Cause != broadshift.CauseUnclassified {
		t.Fatalf("cached history lost incident metadata: %+v", second)
	}
}

func TestBroadShiftComparesTargetWithImmediatePriorCompatibleRun(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}
	at := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	averages := []int64{120_000, 100_000, 120_000}
	var targetID int64
	for runIndex, avg := range averages {
		runID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Duration(runIndex)*time.Hour), fmt.Sprintf("run-%d", runIndex))
		for benchmarkIndex := 0; benchmarkIndex < broadShiftMinBenchmarks; benchmarkIndex++ {
			name := fmt.Sprintf("bench-%02d", benchmarkIndex)
			_, err := database.InsertResult(&db.Result{
				RunID: runID, Category: "render", Name: name,
				MinNs: avg, AvgNs: avg, MaxNs: avg, P50Ns: avg, P95Ns: avg, P99Ns: avg,
				TotalNs: avg, Iterations: 1, SampleCount: 1,
			})
			if err != nil {
				t.Fatalf("insert %s: %v", name, err)
			}
		}
		targetID = runID
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d&window=2&min_points=2&baseline_offset=0", targetID), nil)
	rec := httptest.NewRecorder()
	server.handleRegressions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	var snapshot regressionSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.BroadShift.Detected || snapshot.BroadShift.GeometricChangePercent < 19.9 {
		t.Fatalf("broad shift = %+v; target must be compared with immediate prior 100us run, not older 120us run", snapshot.BroadShift)
	}
}

func TestRegressionEndpointsRejectDFMode(t *testing.T) {
	server := &Server{db: openRegressionHistoryTestDB(t)}
	for _, path := range []string{"/api/regressions?df_mode=baseline", "/api/regressions/history?df_mode=latest"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		if strings.Contains(path, "/history") {
			server.handleRegressionsHistory(rec, req)
		} else {
			server.handleRegressions(rec, req)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
	}
}

func TestRegressionEndpointsRejectSinglePointMinimum(t *testing.T) {
	server := &Server{db: openRegressionHistoryTestDB(t)}
	for _, path := range []string{"/api/regressions?min_points=1", "/api/regressions/history?min_points=1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		if strings.Contains(path, "/history") {
			server.handleRegressionsHistory(rec, req)
		} else {
			server.handleRegressions(rec, req)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
	}
}

func TestRegressionsNoRunsReportsRequestedConfiguration(t *testing.T) {
	server := &Server{db: openRegressionHistoryTestDB(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/regressions?window=7&min_points=2&baseline_offset=1", nil)
	rec := httptest.NewRecorder()
	server.handleRegressions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var response struct {
		Window         int `json:"window"`
		MinPoints      int `json:"min_points"`
		BaselineOffset int `json:"baseline_offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Window != 7 || response.MinPoints != 2 || response.BaselineOffset != 1 {
		t.Fatalf("configuration = %+v, want window=7 min_points=2 baseline_offset=1", response)
	}
}

func TestRegressionHistoryReturnsWhenCacheWriteFails(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}

	insertRegressionHistoryTestRun(t, database, "main", time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC), "a")
	if _, err := database.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatalf("enable query_only: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/regressions/history?branch=main&limit=1", nil)
	rec := httptest.NewRecorder()
	server.handleRegressionsHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response regressionHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ComputedRuns != 1 {
		t.Fatalf("computed_runs = %d, want 1", response.ComputedRuns)
	}
}

func TestTrendRejectsFuzzyNameRequests(t *testing.T) {
	server := &Server{db: openRegressionHistoryTestDB(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/trend?name=frame", nil)
	rec := httptest.NewRecorder()

	server.handleTrend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestLegacyResultIDMapOmitsAmbiguousCompositeKeys(t *testing.T) {
	resultIDs := map[db.BenchmarkKey]int64{
		{Category: "a", Name: "b/c"}:            1,
		{Category: "a/b", Name: "c"}:            2,
		{Category: "safe", Name: "key"}:         3,
		{Category: "first", Name: "duplicate"}:  4,
		{Category: "second", Name: "duplicate"}: 5,
	}

	legacy := legacyResultIDMap(resultIDs)
	if _, ok := legacy["a/b/c"]; ok {
		t.Fatal("ambiguous legacy key must be omitted")
	}
	if legacy["safe/key"] != 3 {
		t.Fatalf("safe legacy key = %d, want 3", legacy["safe/key"])
	}
	if _, ok := legacy["first/duplicate"]; ok {
		t.Fatal("duplicate benchmark names must be omitted for name-only clients")
	}
	if _, ok := legacy["second/duplicate"]; ok {
		t.Fatal("duplicate benchmark names must be omitted for name-only clients")
	}
}
