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
