package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	before := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, regressionDFModeBaseline)
	insertRegressionHistoryTestRun(t, openRegressionHistoryTestDB(t), "main", time.Now(), "future")
	after := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, regressionDFModeBaseline)
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
