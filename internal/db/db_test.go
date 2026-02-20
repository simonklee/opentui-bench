package db

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
