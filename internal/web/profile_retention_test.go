package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"opentui-bench/internal/db"
)

func TestArtifactRetentionRunsOnlyAfterFinalization(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	insertRun := func(commit, runDate string) (int64, int64) {
		t.Helper()
		runID, err := database.InsertRun(&db.Run{
			CommitHash: commit, Branch: "main", RunDate: runDate,
			MachineID: "runner", ZigOptimize: "ReleaseFast",
		})
		if err != nil {
			t.Fatal(err)
		}
		resultID, err := database.InsertResult(&db.Result{
			RunID: runID, Category: "cat", Name: "bench",
			MinNs: 1, AvgNs: 2, MaxNs: 3, P50Ns: 2, P95Ns: 3, P99Ns: 3,
			TotalNs: 2, Iterations: 1, SampleCount: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runID, resultID
	}

	_, oldResultID := insertRun("old", "2026-01-01T00:00:00Z")
	if _, err := database.InsertArtifact(&db.Artifact{
		ResultID: oldResultID, Kind: cpuProfileKind, DataBlob: []byte("old"),
		Metadata: "{}", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	newRunID, newResultID := insertRun("new", "2026-01-02T00:00:00Z")
	server := &Server{
		db: database,
		profileRetention: db.ProfileRetention{
			MaxRuns: 1, MaxBytes: 64,
		},
	}

	upload := httptest.NewRecorder()
	uploadPath := fmt.Sprintf("/api/runs/%d/results/%d/artifacts?kind=%s", newRunID, newResultID, cpuProfileKind)
	server.handleUploadArtifact(upload, httptest.NewRequest(http.MethodPost, uploadPath, strings.NewReader("new")))
	if upload.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", upload.Code, upload.Body.String())
	}
	var oldProfiles int
	if err := database.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE result_id = ? AND kind = ?`, oldResultID, cpuProfileKind).Scan(&oldProfiles); err != nil {
		t.Fatal(err)
	}
	if oldProfiles != 1 {
		t.Fatal("old profile was pruned before the new run was complete")
	}

	finalize := httptest.NewRecorder()
	finalizePath := fmt.Sprintf("/api/runs/%d/artifacts/finalize", newRunID)
	server.handleFinalizeArtifacts(finalize, httptest.NewRequest(http.MethodPost, finalizePath, nil))
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize status = %d: %s", finalize.Code, finalize.Body.String())
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE result_id = ? AND kind = ?`, oldResultID, cpuProfileKind).Scan(&oldProfiles); err != nil {
		t.Fatal(err)
	}
	if oldProfiles != 0 {
		t.Fatal("old profile was retained after finalization")
	}
	if _, err := database.GetArtifact(newResultID, cpuProfileKind); err != nil {
		t.Fatalf("new profile was pruned: %v", err)
	}
}
