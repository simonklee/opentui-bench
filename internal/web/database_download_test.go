package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"opentui-bench/internal/db"
)

func TestDatabaseDownloadReturnsConsistentSnapshot(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	runID := insertRegressionHistoryTestRun(t, database, "main", testTime, "export")
	insertRegressionHistoryTestResult(t, database, runID, 123_456)
	jobID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", CreatedAt: testTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingJob("")
	if err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatalf("claim job: job=%+v err=%v", claimed, err)
	}
	server := &Server{db: database, databaseDownloadSem: make(chan struct{}, 1)}

	request := httptest.NewRequest(http.MethodPost, "/api/database/download", nil)
	recorder := httptest.NewRecorder()
	server.handleDatabaseDownload(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Disposition"); got != `attachment; filename="bench.db"` {
		t.Fatalf("content disposition = %q", got)
	}

	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := os.WriteFile(exportPath, recorder.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := db.OpenReadOnly(exportPath)
	if err != nil {
		t.Fatalf("open exported database: %v", err)
	}
	defer func() { _ = exported.Close() }()
	var count int
	if err := exported.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id = ?`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("exported result count = %d, want 1", count)
	}
	var exportedToken string
	if err := exported.QueryRow(`SELECT claim_token FROM jobs WHERE id = ?`, jobID).Scan(&exportedToken); err != nil {
		t.Fatal(err)
	}
	if exportedToken == "" || exportedToken == claimed.ClaimToken {
		t.Fatalf("export contains a bearer credential: %q", exportedToken)
	}
	if err := database.FailJob(context.Background(), jobID, exportedToken, "stolen"); !errors.Is(err, db.ErrJobClaimLost) {
		t.Fatalf("exported token digest was usable: %v", err)
	}
}

func TestDatabaseDownloadMethodsAndConcurrency(t *testing.T) {
	database := openRegressionHistoryTestDB(t)
	server := &Server{db: database, databaseDownloadSem: make(chan struct{}, 1)}

	recorder := httptest.NewRecorder()
	server.handleDatabaseDownload(recorder, httptest.NewRequest(http.MethodPost, "/api/database/download", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST status = %d; body=%q", recorder.Code, recorder.Body.String())
	}

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		recorder = httptest.NewRecorder()
		server.handleDatabaseDownload(recorder, httptest.NewRequest(method, "/api/database/download", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, recorder.Code, http.StatusMethodNotAllowed)
		}
	}

	server.databaseDownloadSem <- struct{}{}
	recorder = httptest.NewRecorder()
	server.handleDatabaseDownload(recorder, httptest.NewRequest(http.MethodPost, "/api/database/download", nil))
	<-server.databaseDownloadSem
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
}

var testTime = mustTestTime()

func mustTestTime() time.Time {
	return time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
}
