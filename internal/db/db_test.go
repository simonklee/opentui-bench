package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func insertTestResult(t *testing.T, database *DB, runID int64, category, name string) int64 {
	t.Helper()
	id, err := database.InsertResult(&Result{
		RunID: runID, Category: category, Name: name,
		MinNs: 1, AvgNs: 2, MaxNs: 3, P50Ns: 2, P95Ns: 3, P99Ns: 3,
		TotalNs: 10, Iterations: 5, SampleCount: 1,
	})
	if err != nil {
		t.Fatalf("insert result %s/%s: %v", category, name, err)
	}
	return id
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestInsertAndGetJob(t *testing.T) {
	db := openTestDB(t)

	job := &Job{
		Status:      "pending",
		Kind:        "benchmark",
		Branch:      "feature/fast-rope",
		CommitHash:  "abc123def456",
		RepoURL:     "origin",
		Samples:     3,
		Profile:     "cpu",
		Notes:       "testing rope optimization",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		RequestedBy: "simon",
	}

	id, err := db.InsertJob(job)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := db.GetJob(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	if got.Status != "pending" {
		t.Errorf("status = %q, want %q", got.Status, "pending")
	}
	if got.Branch != "feature/fast-rope" {
		t.Errorf("branch = %q, want %q", got.Branch, "feature/fast-rope")
	}
	if got.CommitHash != "abc123def456" {
		t.Errorf("commit_hash = %q, want %q", got.CommitHash, "abc123def456")
	}
	if got.Samples != 3 {
		t.Errorf("samples = %d, want %d", got.Samples, 3)
	}
	if got.Profile != "cpu" {
		t.Errorf("profile = %q, want %q", got.Profile, "cpu")
	}
	if got.Notes != "testing rope optimization" {
		t.Errorf("notes = %q, want %q", got.Notes, "testing rope optimization")
	}
	if got.RequestedBy != "simon" {
		t.Errorf("requested_by = %q, want %q", got.RequestedBy, "simon")
	}
	if got.RunID != nil {
		t.Errorf("run_id = %v, want nil", got.RunID)
	}
}

func TestInsertJobOptionalFields(t *testing.T) {
	db := openTestDB(t)

	job := &Job{
		Status:    "pending",
		Kind:      "benchmark",
		Branch:    "main",
		RepoURL:   "origin",
		Samples:   1,
		Profile:   "none",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	id, err := db.InsertJob(job)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	got, err := db.GetJob(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	if got.CommitHash != "" {
		t.Errorf("commit_hash = %q, want empty", got.CommitHash)
	}
	if got.Notes != "" {
		t.Errorf("notes = %q, want empty", got.Notes)
	}
	if got.RequestedBy != "" {
		t.Errorf("requested_by = %q, want empty", got.RequestedBy)
	}
}

func TestListJobs(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		status := "pending"
		if i < 2 {
			status = "completed"
		}
		branch := "main"
		if i%2 == 0 {
			branch = "feature/x"
		}
		_, err := db.InsertJob(&Job{
			Status:    status,
			Kind:      "benchmark",
			Branch:    branch,
			RepoURL:   "origin",
			Samples:   3,
			Profile:   "cpu",
			CreatedAt: now.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("insert job %d: %v", i, err)
		}
	}

	t.Run("all jobs", func(t *testing.T) {
		jobs, err := db.ListJobs(0, "", "")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs) != 5 {
			t.Fatalf("got %d jobs, want 5", len(jobs))
		}
	})

	t.Run("with limit", func(t *testing.T) {
		jobs, err := db.ListJobs(2, "", "")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs) != 2 {
			t.Fatalf("got %d jobs, want 2", len(jobs))
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		jobs, err := db.ListJobs(0, "completed", "")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs) != 2 {
			t.Fatalf("got %d jobs, want 2", len(jobs))
		}
	})

	t.Run("filter by branch", func(t *testing.T) {
		jobs, err := db.ListJobs(0, "", "feature/x")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs) != 3 {
			t.Fatalf("got %d jobs, want 3", len(jobs))
		}
	})

	t.Run("order is newest first", func(t *testing.T) {
		jobs, err := db.ListJobs(0, "", "")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if jobs[0].CreatedAt <= jobs[len(jobs)-1].CreatedAt {
			t.Errorf("expected newest first, got first=%s last=%s", jobs[0].CreatedAt, jobs[len(jobs)-1].CreatedAt)
		}
	})
}

func TestClaimNextPendingJob(t *testing.T) {
	db := openTestDB(t)

	t.Run("returns nil when no jobs", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil, got job %d", job.ID)
		}
	})

	now := time.Now().UTC()
	_, _ = db.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "branch-a",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: now.Format(time.RFC3339),
	})
	_, _ = db.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "branch-b",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: now.Add(time.Second).Format(time.RFC3339),
	})

	t.Run("claims oldest first", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if job == nil {
			t.Fatal("expected a job, got nil")
		}
		if job.Branch != "branch-a" {
			t.Errorf("branch = %q, want branch-a", job.Branch)
		}
		if job.Status != "running" {
			t.Errorf("status = %q, want running", job.Status)
		}
		if job.StartedAt == "" {
			t.Error("started_at should be set")
		}
	})

	t.Run("skips running jobs", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if job == nil {
			t.Fatal("expected a job, got nil")
		}
		if job.Branch != "branch-b" {
			t.Errorf("branch = %q, want branch-b", job.Branch)
		}
	})

	t.Run("returns nil when all claimed", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil, got job %d", job.ID)
		}
	})
}

func TestCompleteJob(t *testing.T) {
	db := openTestDB(t)

	// Insert a run to reference
	runID, err := db.InsertRun(&Run{
		CommitHash: "abc1234",
		RunDate:    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	jobID, _ := db.InsertJob(&Job{
		Status: "running", Kind: "benchmark", Branch: "main",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	err = db.CompleteJob(jobID, runID)
	if err != nil {
		t.Fatalf("complete job: %v", err)
	}

	got, err := db.GetJob(jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.CompletedAt == "" {
		t.Error("completed_at should be set")
	}
	if got.RunID == nil || *got.RunID != runID {
		t.Errorf("run_id = %v, want %d", got.RunID, runID)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty", got.Error)
	}
}

func TestFailJob(t *testing.T) {
	db := openTestDB(t)

	jobID, _ := db.InsertJob(&Job{
		Status: "running", Kind: "benchmark", Branch: "main",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	err := db.FailJob(jobID, "zig build failed: exit code 1")
	if err != nil {
		t.Fatalf("fail job: %v", err)
	}

	got, err := db.GetJob(jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error != "zig build failed: exit code 1" {
		t.Errorf("error = %q, want error message", got.Error)
	}
	if got.CompletedAt == "" {
		t.Error("completed_at should be set")
	}
}

func TestCancelJob(t *testing.T) {
	db := openTestDB(t)

	jobID, _ := db.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "main",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	t.Run("cancel pending job", func(t *testing.T) {
		err := db.CancelJob(jobID)
		if err != nil {
			t.Fatalf("cancel job: %v", err)
		}

		got, err := db.GetJob(jobID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if got.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", got.Status)
		}
	})

	t.Run("cancel non-pending job fails", func(t *testing.T) {
		err := db.CancelJob(jobID)
		if err == nil {
			t.Fatal("expected error cancelling non-pending job")
		}
	})
}

func TestUpdateJobCommitHash(t *testing.T) {
	db := openTestDB(t)

	jobID, _ := db.InsertJob(&Job{
		Status: "running", Kind: "benchmark", Branch: "main",
		RepoURL: "origin", Samples: 3, Profile: "cpu",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	err := db.UpdateJobCommitHash(jobID, "deadbeef12345678")
	if err != nil {
		t.Fatalf("update commit hash: %v", err)
	}

	got, err := db.GetJob(jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.CommitHash != "deadbeef12345678" {
		t.Errorf("commit_hash = %q, want deadbeef12345678", got.CommitHash)
	}
}

func TestBenchmarkKeyQueriesKeepSameNameCategoriesSeparate(t *testing.T) {
	database := openTestDB(t)
	runID, err := database.InsertRun(&Run{CommitHash: "keys001", Branch: "main", RunDate: "2026-01-01T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := insertTestResult(t, database, runID, "render", "same-name")
	insertTestResult(t, database, runID, "layout", "same-name")

	keys, err := database.GetDistinctBenchmarkKeys([]int64{runID})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []BenchmarkKey{{Category: "layout", Name: "same-name"}, {Category: "render", Name: "same-name"}}
	if len(keys) != len(wantKeys) {
		t.Fatalf("got keys %v, want %v", keys, wantKeys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("keys[%d] = %#v, want %#v", i, keys[i], wantKeys[i])
		}
	}

	results, err := database.GetResultsForBenchmarkInRuns(BenchmarkKey{Category: "render", Name: "same-name"}, []int64{runID})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[runID].ID != firstID {
		t.Fatalf("exact results = %#v, want render result %d", results, firstID)
	}
}

func TestResultIdentityIsUniqueWithinRun(t *testing.T) {
	database := openTestDB(t)
	runID, err := database.InsertRun(&Run{CommitHash: "unique1", RunDate: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	insertTestResult(t, database, runID, "render", "frame")
	if _, err := database.InsertResult(&Result{
		RunID: runID, Category: "render", Name: "frame",
		MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 10, Iterations: 5,
	}); err == nil {
		t.Fatal("expected duplicate benchmark result to be rejected")
	}
}

func TestOpenRejectsAmbiguousExistingResultDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicates.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE results (id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL, category TEXT NOT NULL, name TEXT NOT NULL);
		INSERT INTO results(run_id, category, name) VALUES (1, 'render', 'frame'), (1, 'render', 'frame');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if database, err := Open(path); err == nil {
		_ = database.Close()
		t.Fatal("expected duplicate audit to reject database")
	}
}

func TestUnknownMachineCohortIsIsolated(t *testing.T) {
	database := openTestDB(t)
	oldID, err := database.InsertRun(&Run{CommitHash: "legacy1", Branch: "main", RunDate: "2026-01-01T00:00:00Z", ZigOptimize: "ReleaseFast"})
	if err != nil {
		t.Fatal(err)
	}
	refID, err := database.InsertRun(&Run{CommitHash: "legacy2", Branch: "main", RunDate: "2026-01-02T00:00:00Z", ZigOptimize: "ReleaseFast"})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := database.GetComparableRunsWindow(refID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != refID {
		t.Fatalf("unknown-machine cohort = %v, want only reference %d (not %d)", runs, refID, oldID)
	}
}

func TestFeatureTrendUsesExactCompatibleMainHistoryAsOfCutoff(t *testing.T) {
	database := openTestDB(t)
	insertRun := func(hash, branch, date, machine string) int64 {
		t.Helper()
		id, err := database.InsertRun(&Run{CommitHash: hash, Branch: branch, RunDate: date, MachineID: machine, ZigOptimize: "ReleaseFast"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	pastMain := insertRun("mainpast", "main", "2026-01-01T00:00:00Z", "runner-a")
	otherMachine := insertRun("mainother", "main", "2026-01-02T00:00:00Z", "runner-b")
	feature := insertRun("feature1", "feature/x", "2026-01-03T00:00:00Z", "runner-a")
	futureMain := insertRun("mainfuture", "main", "2026-01-04T00:00:00Z", "runner-a")
	pastResult := insertTestResult(t, database, pastMain, "render", "frame")
	insertTestResult(t, database, pastMain, "layout", "frame")
	insertTestResult(t, database, otherMachine, "render", "frame")
	featureResult := insertTestResult(t, database, feature, "render", "frame")
	insertTestResult(t, database, futureMain, "render", "frame")

	mainRuns, err := database.GetComparableMainRunsWindow(feature, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mainRuns) != 1 || mainRuns[0].ID != pastMain {
		t.Fatalf("feature main history = %v, want only run %d", mainRuns, pastMain)
	}

	trend, err := database.GetTrend(featureResult, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 2 {
		t.Fatalf("trend has %d points, want 2: %#v", len(trend), trend)
	}
	if trend[0].Result.ID != featureResult || trend[1].Result.ID != pastResult {
		t.Fatalf("trend result IDs = [%d, %d], want [%d, %d]", trend[0].Result.ID, trend[1].Result.ID, featureResult, pastResult)
	}
}

func TestGetComparableRunsWindowMainIncludesLegacyBranchRuns(t *testing.T) {
	db := openTestDB(t)

	const (
		machineID   = "bench-runner"
		zigOptimize = "ReleaseFast"
	)

	now := time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)
	seq := 0
	insertRun := func(branch, machine, optimize string, runDate time.Time) int64 {
		t.Helper()
		seq++
		commitHash := fmt.Sprintf("c%06d", seq)
		id, err := db.InsertRun(&Run{
			CommitHash:     commitHash,
			CommitHashFull: commitHash,
			CommitMessage:  "test",
			CommitDate:     runDate.Format(time.RFC3339),
			Branch:         branch,
			RunDate:        runDate.Format(time.RFC3339),
			MachineID:      machine,
			ZigOptimize:    optimize,
		})
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		return id
	}

	legacyOldID := insertRun("", machineID, zigOptimize, now.Add(-5*time.Hour))
	legacyRecentID := insertRun("", machineID, zigOptimize, now.Add(-4*time.Hour))
	mainOlderID := insertRun("main", machineID, zigOptimize, now.Add(-3*time.Hour))
	_ = insertRun("feature/abc", machineID, zigOptimize, now.Add(-2*time.Hour))
	_ = insertRun("main", "other-runner", zigOptimize, now.Add(-90*time.Minute))
	referenceID := insertRun("main", machineID, zigOptimize, now)
	_ = insertRun("main", machineID, zigOptimize, now.Add(1*time.Hour))

	runs, err := db.GetComparableRunsWindow(referenceID, 10)
	if err != nil {
		t.Fatalf("get comparable runs: %v", err)
	}

	want := []int64{referenceID, mainOlderID, legacyRecentID, legacyOldID}
	if len(runs) != len(want) {
		t.Fatalf("got %d runs, want %d", len(runs), len(want))
	}
	for i := range want {
		if runs[i].ID != want[i] {
			t.Fatalf("runs[%d].id = %d, want %d", i, runs[i].ID, want[i])
		}
	}
}

func TestGetComparableRunsWindowLegacyReferenceIncludesMain(t *testing.T) {
	db := openTestDB(t)

	const (
		machineID   = "bench-runner"
		zigOptimize = "ReleaseFast"
	)

	now := time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)
	seq := 0
	insertRun := func(branch string, runDate time.Time) int64 {
		t.Helper()
		seq++
		commitHash := fmt.Sprintf("d%06d", seq)
		id, err := db.InsertRun(&Run{
			CommitHash:     commitHash,
			CommitHashFull: commitHash,
			CommitMessage:  "test",
			CommitDate:     runDate.Format(time.RFC3339),
			Branch:         branch,
			RunDate:        runDate.Format(time.RFC3339),
			MachineID:      machineID,
			ZigOptimize:    zigOptimize,
		})
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		return id
	}

	mainOlderID := insertRun("main", now.Add(-3*time.Hour))
	legacyOlderID := insertRun("", now.Add(-2*time.Hour))
	referenceID := insertRun("", now.Add(-1*time.Hour))

	runs, err := db.GetComparableRunsWindow(referenceID, 10)
	if err != nil {
		t.Fatalf("get comparable runs: %v", err)
	}

	want := []int64{referenceID, legacyOlderID, mainOlderID}
	if len(runs) != len(want) {
		t.Fatalf("got %d runs, want %d", len(runs), len(want))
	}
	for i := range want {
		if runs[i].ID != want[i] {
			t.Fatalf("runs[%d].id = %d, want %d", i, runs[i].ID, want[i])
		}
	}
}

func TestGetComparableMainRunsWindowOrdersRFC3339ByInstant(t *testing.T) {
	database := openTestDB(t)

	insertRun := func(hash, branch, runDate string) int64 {
		t.Helper()
		id, err := database.InsertRun(&Run{
			CommitHash: hash, Branch: branch, RunDate: runDate,
			MachineID: "runner", ZigOptimize: "ReleaseFast",
		})
		if err != nil {
			t.Fatalf("insert run %s: %v", hash, err)
		}
		return id
	}

	priorID := insertRun("prior", "main", "2026-10-25T02:50:00+02:00")            // 00:50 UTC
	featureID := insertRun("feature", "feature/dst", "2026-10-25T02:10:00+01:00") // 01:10 UTC
	_ = insertRun("future", "main", "2026-10-25T01:20:00Z")

	runs, err := database.GetComparableMainRunsWindow(featureID, 10)
	if err != nil {
		t.Fatalf("get comparable main runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != priorID {
		t.Fatalf("comparable run IDs = %+v, want [%d]", runs, priorID)
	}
}

func TestListRunsForBranchMainIncludesLegacyValues(t *testing.T) {
	db := openTestDB(t)

	now := time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC)
	seq := 0
	insertRun := func(branch string, at time.Time) int64 {
		t.Helper()
		seq++
		commitHash := fmt.Sprintf("m%06d", seq)
		id, err := db.InsertRun(&Run{
			CommitHash:     commitHash,
			CommitHashFull: commitHash,
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
		return id
	}

	legacyID := insertRun("", now.Add(-3*time.Hour))
	mainOldID := insertRun("main", now.Add(-2*time.Hour))
	_ = insertRun("feature/cache", now.Add(-time.Hour))
	mainLatestID := insertRun("main", now)

	runs, err := db.ListRunsForBranch("main", 10)
	if err != nil {
		t.Fatalf("list runs for main: %v", err)
	}

	want := []int64{mainLatestID, mainOldID, legacyID}
	if len(runs) != len(want) {
		t.Fatalf("got %d runs, want %d", len(runs), len(want))
	}
	for i, id := range want {
		if runs[i].ID != id {
			t.Fatalf("runs[%d].id = %d, want %d", i, runs[i].ID, id)
		}
	}

	limited, err := db.ListRunsForBranch("main", 2)
	if err != nil {
		t.Fatalf("list runs for main with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("got %d runs, want 2", len(limited))
	}
	if limited[0].ID != mainLatestID || limited[1].ID != mainOldID {
		t.Fatalf("unexpected limited order: got [%d, %d]", limited[0].ID, limited[1].ID)
	}
}

func TestGetLatestRunForBranchBreaksTimestampTiesByID(t *testing.T) {
	database := openTestDB(t)
	runDate := "2026-02-12T12:00:00Z"
	firstID, err := database.InsertRun(&Run{CommitHash: "tie-1", Branch: "main", RunDate: runDate})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := database.InsertRun(&Run{CommitHash: "tie-2", Branch: "main", RunDate: runDate})
	if err != nil {
		t.Fatal(err)
	}

	latest, err := database.GetLatestRunForBranch("main")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != secondID {
		t.Fatalf("latest run ID = %d, want %d (first was %d)", latest.ID, secondID, firstID)
	}
}

func TestRegressionCacheGenerationInvalidation(t *testing.T) {
	db := openTestDB(t)

	runID, err := db.InsertRun(&Run{
		CommitHash:     "cache001",
		CommitHashFull: "cache001",
		CommitMessage:  "cache test",
		CommitDate:     time.Now().UTC().Format(time.RFC3339),
		Branch:         "main",
		RunDate:        time.Now().UTC().Format(time.RFC3339),
		MachineID:      "runner",
		ZigOptimize:    "ReleaseFast",
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	key := RegressionCacheKey{
		RunID:          runID,
		Branch:         "main",
		Window:         30,
		MinPoints:      5,
		BaselineOffset: 3,
		DFMode:         "baseline",
	}

	first := &RegressionCacheEntry{
		Key:           key,
		GenerationKey: "gen-a",
		ResponseJSON:  `{"run_id":1,"regressions":[{"name":"bench-a"}]}`,
	}
	if err := db.UpsertRegressionCache(first); err != nil {
		t.Fatalf("upsert first cache entry: %v", err)
	}

	got, err := db.GetRegressionCache(key, "gen-a")
	if err != nil {
		t.Fatalf("get cache for gen-a: %v", err)
	}
	if got == nil {
		t.Fatal("expected cache hit for gen-a")
	}
	if got.ResponseJSON != first.ResponseJSON {
		t.Fatalf("response_json = %q, want %q", got.ResponseJSON, first.ResponseJSON)
	}

	miss, err := db.GetRegressionCache(key, "gen-b")
	if err != nil {
		t.Fatalf("get cache for gen-b: %v", err)
	}
	if miss != nil {
		t.Fatalf("expected cache miss for gen-b, got %+v", miss)
	}

	second := &RegressionCacheEntry{
		Key:           key,
		GenerationKey: "gen-b",
		ResponseJSON:  `{"run_id":1,"regressions":[{"name":"bench-b"}]}`,
	}
	if err := db.UpsertRegressionCache(second); err != nil {
		t.Fatalf("upsert second cache entry: %v", err)
	}

	oldGen, err := db.GetRegressionCache(key, "gen-a")
	if err != nil {
		t.Fatalf("get cache for stale gen-a: %v", err)
	}
	if oldGen != nil {
		t.Fatal("expected stale generation to be invalidated")
	}

	newGen, err := db.GetRegressionCache(key, "gen-b")
	if err != nil {
		t.Fatalf("get cache for fresh gen-b: %v", err)
	}
	if newGen == nil {
		t.Fatal("expected cache hit for gen-b")
	}
	if newGen.ResponseJSON != second.ResponseJSON {
		t.Fatalf("response_json = %q, want %q", newGen.ResponseJSON, second.ResponseJSON)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
