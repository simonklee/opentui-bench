package web

import (
	"fmt"
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
