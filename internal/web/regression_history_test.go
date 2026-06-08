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

func TestRegressionCacheBaselineAnchorTracksLatestMainForFeatureBranch(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database}

	anchor, err := server.regressionCacheBaselineAnchor("feature/cache")
	if err != nil {
		t.Fatalf("baseline anchor with no main runs: %v", err)
	}
	if anchor != "main:none" {
		t.Fatalf("anchor with no main runs = %q, want %q", anchor, "main:none")
	}

	at := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	firstMainID := insertRegressionHistoryTestRun(t, database, "main", at, "a")

	firstAnchor, err := server.regressionCacheBaselineAnchor("feature/cache")
	if err != nil {
		t.Fatalf("baseline anchor after first main run: %v", err)
	}
	wantFirst := fmt.Sprintf("main:%d", firstMainID)
	if firstAnchor != wantFirst {
		t.Fatalf("anchor after first main run = %q, want %q", firstAnchor, wantFirst)
	}

	secondMainID := insertRegressionHistoryTestRun(t, database, "main", at.Add(time.Hour), "b")

	secondAnchor, err := server.regressionCacheBaselineAnchor("feature/cache")
	if err != nil {
		t.Fatalf("baseline anchor after second main run: %v", err)
	}
	wantSecond := fmt.Sprintf("main:%d", secondMainID)
	if secondAnchor != wantSecond {
		t.Fatalf("anchor after second main run = %q, want %q", secondAnchor, wantSecond)
	}
	if firstAnchor == secondAnchor {
		t.Fatal("expected baseline anchor to change when latest main run changes")
	}
}

func TestRegressionCacheGenerationKeyIncludesBaselineAnchor(t *testing.T) {
	server := &Server{}

	first := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, regressionDFModeBaseline, "main:100")
	second := server.regressionCacheGenerationKey("feature/cache", 30, 5, 3, regressionDFModeBaseline, "main:101")
	if first == second {
		t.Fatal("expected generation keys to differ when baseline anchor changes")
	}
}

func TestRegressionCacheGenerationKeyIsStable(t *testing.T) {
	server := &Server{}

	got := server.regressionCacheGenerationKey("main", 30, 5, 3, regressionDFModeBaseline, "self")
	const want = "4d04bf945ae93ab72caf03d2112da50e5324e76eb5317cb7f2f882aeab19d5ec"
	if got != want {
		t.Fatalf("generation key = %q, want %q", got, want)
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
