package db

import (
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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
