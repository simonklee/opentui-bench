package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opentui-bench/internal/joblease"
	"opentui-bench/internal/jsbench"
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

func insertProfileTestRun(t *testing.T, database *DB, runDate string, profile []byte) (int64, int64) {
	t.Helper()
	runID, err := database.InsertRun(&Run{
		CommitHash:  "commit-" + runDate,
		Branch:      "main",
		RunDate:     runDate,
		MachineID:   "test-machine",
		ZigOptimize: "ReleaseFast",
	})
	if err != nil {
		t.Fatalf("insert profile test run: %v", err)
	}
	resultID := insertTestResult(t, database, runID, "render", "profiled")
	if _, err := database.InsertArtifact(&Artifact{
		ResultID: resultID, Kind: "cpu.pprof", DataBlob: profile,
		Metadata: "{}", CreatedAt: runDate,
	}); err != nil {
		t.Fatalf("insert profile artifact: %v", err)
	}
	return runID, resultID
}

func TestPruneProfileDataPreservesBenchmarkHistory(t *testing.T) {
	database := openTestDB(t)
	oldRunID, oldResultID := insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", []byte("old!"))
	middleRunID, middleResultID := insertProfileTestRun(t, database, "2026-01-02T00:00:00Z", []byte("mid!"))
	newRunID, newResultID := insertProfileTestRun(t, database, "2026-01-03T00:00:00Z", []byte("new!"))

	for _, artifact := range []Artifact{
		{ResultID: oldResultID, Kind: "cpu.flamegraph.svg", DataBlob: []byte("derived flamegraph"), Metadata: "{}", CreatedAt: "2026-01-01T00:00:00Z"},
		{ResultID: middleResultID, Kind: "cpu.callgraph.svg", DataBlob: []byte("derived callgraph"), Metadata: "{}", CreatedAt: "2026-01-02T00:00:00Z"},
		{ResultID: oldResultID, Kind: "diagnostic.txt", DataBlob: []byte("keep unknown artifacts"), Metadata: "{}", CreatedAt: "2026-01-01T00:00:00Z"},
	} {
		artifact := artifact
		if _, err := database.InsertArtifact(&artifact); err != nil {
			t.Fatalf("insert %s: %v", artifact.Kind, err)
		}
	}
	if _, err := database.Exec(`
		INSERT INTO flamegraphs (run_id, benchmark_name, folded_stacks_gz, sampling_freq, created_at)
		VALUES (?, 'profiled', ?, 997, ?)`, oldRunID, []byte("legacy"), "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert legacy flamegraph: %v", err)
	}

	pruned, err := database.PruneProfileData(ProfileRetention{MaxRuns: 2, MaxBytes: 100})
	if err != nil {
		t.Fatalf("prune profile data: %v", err)
	}
	if pruned.ProfileRunsRetained != 2 || pruned.BytesRetained != 8 {
		t.Fatalf("retained runs/bytes = %d/%d, want 2/8", pruned.ProfileRunsRetained, pruned.BytesRetained)
	}

	for _, resultID := range []int64{middleResultID, newResultID} {
		if _, err := database.GetArtifact(resultID, "cpu.pprof"); err != nil {
			t.Errorf("retained result %d profile: %v", resultID, err)
		}
	}
	if _, err := database.GetArtifact(oldResultID, "cpu.pprof"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old profile error = %v, want sql.ErrNoRows", err)
	}
	for resultID, kind := range map[int64]string{
		oldResultID:    "cpu.flamegraph.svg",
		middleResultID: "cpu.callgraph.svg",
	} {
		if _, err := database.GetArtifact(resultID, kind); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("derived artifact %s error = %v, want sql.ErrNoRows", kind, err)
		}
	}
	if _, err := database.GetArtifact(oldResultID, "diagnostic.txt"); err != nil {
		t.Errorf("unknown artifact was not preserved: %v", err)
	}

	var runCount, resultCount, legacyCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM flamegraphs`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 3 || resultCount != 3 || legacyCount != 0 {
		t.Errorf("runs/results/legacy = %d/%d/%d, want 3/3/0", runCount, resultCount, legacyCount)
	}
	for _, runID := range []int64{oldRunID, middleRunID, newRunID} {
		if _, err := database.GetRun(runID); err != nil {
			t.Errorf("retained run %d: %v", runID, err)
		}
	}
}

func TestPruneProfileDataEnforcesByteBound(t *testing.T) {
	database := openTestDB(t)
	oldRunID, oldResultID := insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", []byte("old!"))
	_, middleResultID := insertProfileTestRun(t, database, "2026-01-02T00:00:00Z", []byte("mid!"))
	_, newResultID := insertProfileTestRun(t, database, "2026-01-03T00:00:00Z", []byte("new!"))

	pruned, err := database.PruneProfileData(ProfileRetention{MaxRuns: 10, MaxBytes: 7})
	if err != nil {
		t.Fatalf("prune profile data: %v", err)
	}
	if pruned.ProfileRunsRetained != 1 || pruned.BytesRetained != 4 {
		t.Fatalf("retained runs/bytes = %d/%d, want 1/4", pruned.ProfileRunsRetained, pruned.BytesRetained)
	}
	if _, err := database.GetArtifact(newResultID, "cpu.pprof"); err != nil {
		t.Errorf("newest profile: %v", err)
	}
	for _, resultID := range []int64{oldResultID, middleResultID} {
		if _, err := database.GetArtifact(resultID, "cpu.pprof"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("pruned result %d profile error = %v, want sql.ErrNoRows", resultID, err)
		}
	}
	if _, err := database.GetRun(oldRunID); err != nil {
		t.Errorf("old benchmark history was deleted: %v", err)
	}
}

func TestPruneProfileDataDropsIncompleteProfileRuns(t *testing.T) {
	database := openTestDB(t)
	_, completeResultID := insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", []byte("complete"))
	partialRunID, partialResultID := insertProfileTestRun(t, database, "2026-01-02T00:00:00Z", []byte("partial"))
	insertTestResult(t, database, partialRunID, "render", "missing-profile")

	pruned, err := database.PruneProfileData(ProfileRetention{MaxRuns: 1, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if pruned.ProfileRunsRetained != 1 || pruned.BytesRetained != int64(len("complete")) {
		t.Fatalf("retained runs/bytes = %d/%d, want 1/%d", pruned.ProfileRunsRetained, pruned.BytesRetained, len("complete"))
	}
	if _, err := database.GetArtifact(completeResultID, "cpu.pprof"); err != nil {
		t.Fatalf("complete profile set was pruned: %v", err)
	}
	if _, err := database.GetArtifact(partialResultID, "cpu.pprof"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial profile error = %v, want sql.ErrNoRows", err)
	}
	results, err := database.GetResultsForRun(partialRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("partial run result count = %d, want 2", len(results))
	}
}

func TestFinalizeProfileDataPreservesOtherIncompleteRuns(t *testing.T) {
	database := openTestDB(t)
	completeRunID, completeResultID := insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", []byte("complete"))
	partialRunID, partialResultID := insertProfileTestRun(t, database, "2026-01-02T00:00:00Z", []byte("partial"))
	insertTestResult(t, database, partialRunID, "render", "still-uploading")
	retention := ProfileRetention{MaxRuns: 1, MaxBytes: 100}

	_, complete, err := database.FinalizeProfileData(completeRunID, retention)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("complete target reported incomplete")
	}
	if _, err := database.GetArtifact(completeResultID, "cpu.pprof"); err != nil {
		t.Fatalf("complete target profile was pruned: %v", err)
	}
	if _, err := database.GetArtifact(partialResultID, "cpu.pprof"); err != nil {
		t.Fatalf("concurrent partial profile was pruned: %v", err)
	}

	_, complete, err = database.FinalizeProfileData(partialRunID, retention)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("partial target reported complete")
	}
	if _, err := database.GetArtifact(partialResultID, "cpu.pprof"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("finalized partial profile error = %v, want sql.ErrNoRows", err)
	}
}

func TestPruneProfileDataRejectsInvalidBounds(t *testing.T) {
	database := openTestDB(t)
	for _, retention := range []ProfileRetention{
		{MaxRuns: 0, MaxBytes: 1},
		{MaxRuns: 1, MaxBytes: 0},
	} {
		if _, err := database.PruneProfileData(retention); err == nil {
			t.Errorf("PruneProfileData(%+v) succeeded, want error", retention)
		}
	}
}

func TestVacuumReclaimsPrunedProfilePages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", make([]byte, 2<<20))
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.PruneProfileData(ProfileRetention{MaxRuns: 1, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if err := database.Vacuum(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("database size after vacuum = %d, want less than %d", after.Size(), before.Size())
	}
}

func TestCompactBackupExcludesFreePagesAndPreservesSource(t *testing.T) {
	database := openTestDB(t)
	runID, _ := insertProfileTestRun(t, database, "2026-01-01T00:00:00Z", make([]byte, 2<<20))
	if _, err := database.PruneProfileData(ProfileRetention{MaxRuns: 1, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	before, err := database.StorageStats()
	if err != nil {
		t.Fatal(err)
	}
	if before.FreeBytes == 0 {
		t.Fatal("test database has no free pages")
	}

	destination := filepath.Join(t.TempDir(), "compact.db")
	if err := database.CompactBackup(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	exported, err := OpenReadOnly(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exported.Close() }()
	after, err := exported.StorageStats()
	if err != nil {
		t.Fatal(err)
	}
	if after.FreeBytes != 0 || after.AllocatedBytes >= before.AllocatedBytes {
		t.Fatalf("compact storage = %+v, source storage = %+v", after, before)
	}
	if _, err := exported.GetRun(runID); err != nil {
		t.Fatalf("exported benchmark history: %v", err)
	}

	sourceAfter, err := database.StorageStats()
	if err != nil {
		t.Fatal(err)
	}
	if sourceAfter != before {
		t.Fatalf("compact backup changed source storage: before=%+v after=%+v", before, sourceAfter)
	}
}

func TestCompactBackupRejectsExistingDestinationAndCleansUpCancellation(t *testing.T) {
	database := openTestDB(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := database.CompactBackup(context.Background(), existing); err == nil {
		t.Fatal("compact backup replaced an existing destination")
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing destination changed: contents=%q err=%v", contents, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := filepath.Join(dir, "cancelled.db")
	if err := database.CompactBackup(ctx, cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled compact backup error = %v", err)
	}
	if _, err := os.Stat(cancelled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled destination remains: %v", err)
	}
}

func TestDefaultProfileRetentionBounds(t *testing.T) {
	if DefaultProfileRunsMax != 50 || DefaultProfileBytesMax != 128<<20 {
		t.Fatalf("default profile retention = %d runs/%d bytes", DefaultProfileRunsMax, DefaultProfileBytesMax)
	}
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

func TestListJobsFiltersBeforeLimit(t *testing.T) {
	database := openTestDB(t)
	_, err := database.InsertJob(&Job{Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "none",
		CreatedAt: "2026-08-03T00:00:00Z", RequestedBy: "worker", BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: jsbench.Protocol, ManifestHash: jsbench.ManifestDigest})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertJob(&Job{Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu",
		CreatedAt: "2026-08-04T00:00:00Z", RequestedBy: "other", BenchmarkKind: "zig"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := database.ListJobsFiltered(1, "failed", "", "js", "worker", jsbench.Suite, jsbench.Protocol,
		jsbench.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].BenchmarkKind != "js" || jobs[0].RequestedBy != "worker" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestClaimNextPendingJob(t *testing.T) {
	db := openTestDB(t)

	t.Run("returns nil when no jobs", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob("")
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
		job, err := db.ClaimNextPendingJob("")
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
		if len(job.ClaimToken) != 64 {
			t.Errorf("claim token length = %d, want 64", len(job.ClaimToken))
		}
		var storedToken string
		if err := db.QueryRow(`SELECT claim_token FROM jobs WHERE id = ?`, job.ID).Scan(&storedToken); err != nil {
			t.Fatal(err)
		}
		if len(storedToken) != 64 || storedToken == job.ClaimToken {
			t.Fatalf("stored claim credential = %q, want a distinct SHA-256 digest", storedToken)
		}
		storedJob, err := db.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if storedJob.ClaimToken != "" {
			t.Fatalf("GetJob exposed stored claim digest: %q", storedJob.ClaimToken)
		}
		if err := db.ReleaseJob(context.Background(), job.ID, storedToken); !errors.Is(err, ErrJobClaimLost) {
			t.Fatalf("stored digest used as bearer token: %v", err)
		}
	})

	t.Run("skips running jobs", func(t *testing.T) {
		job, err := db.ClaimNextPendingJob("")
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
		job, err := db.ClaimNextPendingJob("")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if job != nil {
			t.Fatalf("expected nil, got job %d", job.ID)
		}
	})
}

func insertStartedTestJob(t *testing.T, database *DB, job Job, startedAt string) int64 {
	t.Helper()
	job.Kind = "benchmark"
	job.Branch = "main"
	job.Samples = 3
	job.CreatedAt = timeNow()
	jobID, err := database.InsertJob(&job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET started_at = ? WHERE id = ?`, startedAt, jobID); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func claimTestJob(t *testing.T, database *DB) *Job {
	t.Helper()
	jobID, err := database.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: timeNow(),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingJob("")
	if err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatalf("claim job: job=%+v err=%v", claimed, err)
	}
	return claimed
}

func TestClaimNextPendingJobRecoversExpiredRunningJobWithoutPendingJobs(t *testing.T) {
	database := openTestDB(t)
	startedAt := time.Now().UTC().Add(-JobLeaseDuration - time.Hour).Format(time.RFC3339)
	jobID := insertStartedTestJob(t, database, Job{Status: "running", Profile: "cpu"}, startedAt)

	job, err := database.ClaimNextPendingJob("")
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != jobID || job.Status != "running" || job.StartedAt == startedAt {
		t.Fatalf("recovered job = %+v, want newly leased job %d", job, jobID)
	}
}

func TestClaimNextPendingJobDoesNotStealNormalLongRunningJob(t *testing.T) {
	database := openTestDB(t)
	startedAt := time.Now().UTC().Add(-JobLeaseDuration + time.Hour).Format(time.RFC3339)
	jobID := insertStartedTestJob(t, database, Job{Status: "running", Profile: "cpu"}, startedAt)

	job, err := database.ClaimNextPendingJob("")
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("claimed active long-running job: %+v", job)
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || stored.StartedAt != startedAt {
		t.Fatalf("active job changed: %+v", stored)
	}
}

func TestClaimNextPendingJobKeepsCancelledJobTerminal(t *testing.T) {
	database := openTestDB(t)
	jobID := insertStartedTestJob(t, database, Job{Status: "cancelled", Profile: "cpu"},
		time.Now().UTC().Add(-2*JobLeaseDuration).Format(time.RFC3339))

	job, err := database.ClaimNextPendingJob("")
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("claimed cancelled job: %+v", job)
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "cancelled" {
		t.Fatalf("cancelled job status = %q", stored.Status)
	}
}

func TestClaimNextPendingJobFiltersKindAfterRecoveringStaleJobs(t *testing.T) {
	database := openTestDB(t)
	jsID := insertStartedTestJob(t, database, Job{
		Status: "running", Profile: "none",
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: jsbench.Protocol, ManifestHash: jsbench.ManifestDigest,
	}, time.Now().UTC().Add(-2*JobLeaseDuration).Format(time.RFC3339))
	zigID, err := database.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu", CreatedAt: timeNow(),
		BenchmarkKind: "zig",
	})
	if err != nil {
		t.Fatal(err)
	}

	job, err := database.ClaimNextPendingJob("zig")
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != zigID {
		t.Fatalf("claimed job = %+v, want Zig job %d", job, zigID)
	}
	javascript, err := database.GetJob(jsID)
	if err != nil {
		t.Fatal(err)
	}
	if javascript.Status != "pending" || javascript.StartedAt != "" {
		t.Fatalf("recovered JavaScript job = %+v, want pending", javascript)
	}
}

func TestCompleteJob(t *testing.T) {
	db := openTestDB(t)

	// Insert a run to reference
	runID, err := db.InsertRun(&Run{
		CommitHash:     "abc1234",
		CommitHashFull: "abc1234-full",
		RunDate:        time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	claimed := claimTestJob(t, db)
	if err := db.UpdateJobCommitHash(context.Background(), claimed.ID, claimed.ClaimToken, "abc1234-full"); err != nil {
		t.Fatal(err)
	}

	err = db.CompleteJob(context.Background(), claimed.ID, claimed.ClaimToken, runID)
	if err != nil {
		t.Fatalf("complete job: %v", err)
	}

	got, err := db.GetJob(claimed.ID)
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

func TestCompleteJobRejectsWrongCommitOrBenchmarkIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		run  Run
	}{
		{name: "commit", run: Run{CommitHash: "other", CommitHashFull: "other"}},
		{name: "kind", run: Run{CommitHash: "wanted", CommitHashFull: "wanted", BenchmarkKind: "js"}},
		{name: "suite", run: Run{CommitHash: "wanted", CommitHashFull: "wanted", BenchmarkSuite: "other"}},
		{name: "protocol", run: Run{CommitHash: "wanted", CommitHashFull: "wanted", ProtocolVersion: 2}},
		{name: "manifest", run: Run{CommitHash: "wanted", CommitHashFull: "wanted", ManifestHash: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDB(t)
			claimed := claimTestJob(t, database)
			if err := database.UpdateJobCommitHash(context.Background(), claimed.ID, claimed.ClaimToken, "wanted"); err != nil {
				t.Fatal(err)
			}
			test.run.RunDate = timeNow()
			runID, err := database.InsertRun(&test.run)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteJob(context.Background(), claimed.ID, claimed.ClaimToken, runID); !errors.Is(err, ErrJobRunMismatch) {
				t.Fatalf("CompleteJob error = %v, want ErrJobRunMismatch", err)
			}
			stored, err := database.GetJob(claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != "running" || stored.RunID != nil {
				t.Fatalf("mismatched run completed job: %+v", stored)
			}
		})
	}
}

func TestClaimedJobTransitionsHonorContextCancellation(t *testing.T) {
	database := openTestDB(t)
	claimed := claimTestJob(t, database)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.FailJob(ctx, claimed.ID, claimed.ClaimToken, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FailJob error = %v, want context.Canceled", err)
	}
	stored, err := database.GetJob(claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" {
		t.Fatalf("cancelled transition changed job: %+v", stored)
	}
}

func TestFailJob(t *testing.T) {
	db := openTestDB(t)

	claimed := claimTestJob(t, db)

	err := db.FailJob(context.Background(), claimed.ID, claimed.ClaimToken, "zig build failed: exit code 1")
	if err != nil {
		t.Fatalf("fail job: %v", err)
	}

	got, err := db.GetJob(claimed.ID)
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

	claimed := claimTestJob(t, db)

	err := db.UpdateJobCommitHash(context.Background(), claimed.ID, claimed.ClaimToken, "deadbeef12345678")
	if err != nil {
		t.Fatalf("update commit hash: %v", err)
	}

	got, err := db.GetJob(claimed.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.CommitHash != "deadbeef12345678" {
		t.Errorf("commit_hash = %q, want deadbeef12345678", got.CommitHash)
	}
}

func TestStaleJobClaimCannotMutateReclaimedJob(t *testing.T) {
	database := openTestDB(t)
	jobID, err := database.InsertJob(&Job{
		Status: "pending", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: timeNow(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldClaim, err := database.ClaimNextPendingJob("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET started_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-JobLeaseDuration-time.Hour).Format(time.RFC3339), jobID); err != nil {
		t.Fatal(err)
	}
	newClaim, err := database.ClaimNextPendingJob("")
	if err != nil {
		t.Fatal(err)
	}
	if newClaim == nil || newClaim.ID != jobID || newClaim.ClaimToken == oldClaim.ClaimToken {
		t.Fatalf("reclaimed job = %+v, old token = %q", newClaim, oldClaim.ClaimToken)
	}

	runID, err := database.InsertRun(&Run{CommitHash: "abc", RunDate: timeNow()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, mutate := range map[string]func() error{
		"commit hash": func() error {
			return database.UpdateJobCommitHash(ctx, jobID, oldClaim.ClaimToken, "stale")
		},
		"complete": func() error { return database.CompleteJob(ctx, jobID, oldClaim.ClaimToken, runID) },
		"fail":     func() error { return database.FailJob(ctx, jobID, oldClaim.ClaimToken, "stale") },
		"release":  func() error { return database.ReleaseJob(ctx, jobID, oldClaim.ClaimToken) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrJobClaimLost) {
				t.Fatalf("error = %v, want ErrJobClaimLost", err)
			}
		})
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || stored.ClaimToken != "" || stored.CommitHash != "" || stored.RunID != nil {
		t.Fatalf("stale worker mutated reclaimed job: %+v", stored)
	}
	if err := database.ReleaseJob(ctx, jobID, newClaim.ClaimToken); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := database.ClaimNextPendingJob("")
	if err != nil || reclaimed == nil || reclaimed.ClaimToken == newClaim.ClaimToken {
		t.Fatalf("claim after release = %+v, err=%v", reclaimed, err)
	}
}

func TestLegacyTokenlessJobTransitionsEndAfterReclaim(t *testing.T) {
	database := openTestDB(t)
	unmarkedID := insertStartedTestJob(t, database, Job{Status: "running", Profile: "cpu"}, timeNow())
	if err := database.FailJob(context.Background(), unmarkedID, "", "not migrated"); !errors.Is(err, ErrJobClaimLost) {
		t.Fatalf("unmarked tokenless update = %v, want ErrJobClaimLost", err)
	}
	jobID := insertStartedTestJob(t, database, Job{Status: "running", Profile: "cpu"}, timeNow())
	if _, err := database.Exec(`UPDATE jobs SET legacy_tokenless = 1 WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := database.UpdateJobCommitHash(ctx, jobID, "", "wanted"); err != nil {
		t.Fatalf("legacy commit update: %v", err)
	}

	runID, err := database.InsertRun(&Run{CommitHash: "wanted", CommitHashFull: "wanted", RunDate: timeNow()})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteJob(ctx, jobID, "", runID); err != nil {
		t.Fatalf("legacy completion: %v", err)
	}

	reclaimedID := insertStartedTestJob(t, database, Job{Status: "running", Profile: "cpu"},
		time.Now().UTC().Add(-JobLeaseDuration-time.Hour).Format(time.RFC3339))
	if _, err := database.Exec(`UPDATE jobs SET legacy_tokenless = 1 WHERE id = ?`, reclaimedID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := database.ClaimNextPendingJob("")
	if err != nil || reclaimed == nil || reclaimed.ID != reclaimedID || reclaimed.ClaimToken == "" {
		t.Fatalf("reclaimed legacy job = %+v, err=%v", reclaimed, err)
	}
	if err := database.FailJob(ctx, reclaimedID, "", "old worker"); !errors.Is(err, ErrJobClaimLost) {
		t.Fatalf("tokenless update after reclaim = %v, want ErrJobClaimLost", err)
	}
	if err := database.FailJob(ctx, reclaimedID, reclaimed.ClaimToken, "new worker"); err != nil {
		t.Fatalf("claimed failure: %v", err)
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
	} else if !strings.Contains(err.Error(), "duplicate groups") {
		t.Fatalf("duplicate error = %q, want actionable audit", err)
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

func TestDeletingBaselineRunInvalidatesRegressionCache(t *testing.T) {
	database := openTestDB(t)
	baselineID, err := database.InsertRun(&Run{CommitHash: "baseline", RunDate: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := database.InsertRun(&Run{CommitHash: "target", RunDate: "2026-01-02T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	insertTestResult(t, database, baselineID, "render", "frame")
	insertTestResult(t, database, targetID, "render", "frame")
	fingerprintBefore, err := database.RegressionDataFingerprint(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE results SET std_dev_ns = std_dev_ns + 1 WHERE run_id = ?`, targetID); err != nil {
		t.Fatal(err)
	}
	fingerprintAfterSummaryChange, err := database.RegressionDataFingerprint(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintAfterSummaryChange == fingerprintBefore {
		t.Fatal("expected summary correction to change target data fingerprint")
	}
	fingerprintBefore = fingerprintAfterSummaryChange
	key := RegressionCacheKey{RunID: targetID, Branch: "main", Window: 30, MinPoints: 5, BaselineOffset: 3}
	if err := database.UpsertRegressionCache(&RegressionCacheEntry{
		Key: key, GenerationKey: "generation", ResponseJSON: `{"run_id":2}`,
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteRun(baselineID); err != nil {
		t.Fatal(err)
	}
	fingerprintAfter, err := database.RegressionDataFingerprint(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintAfter == fingerprintBefore {
		t.Fatal("expected baseline deletion to change target data fingerprint")
	}
	entry, err := database.GetRegressionCache(key, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Fatal("expected baseline deletion to invalidate cached target snapshot")
	}

	if _, err := database.InsertRun(&Run{CommitHash: "bulk-baseline", RunDate: "2026-01-01T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRegressionCache(&RegressionCacheEntry{
		Key: key, GenerationKey: "generation", ResponseJSON: `{"run_id":2}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeleteRunsBefore("2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	entry, err = database.GetRegressionCache(key, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Fatal("expected bulk baseline deletion to invalidate cached target snapshot")
	}
}

func TestRegressionDataFingerprintsMatchIndividualFingerprints(t *testing.T) {
	database := openTestDB(t)
	runDates := []string{
		"2026-01-01T00:00:00Z",
		"2026-01-02T00:00:00Z",
		"2026-01-02T00:00:00Z",
		"2026-01-03T00:00:00Z",
	}
	runIDs := make([]int64, 0, len(runDates))
	for i, runDate := range runDates {
		runID, err := database.InsertRun(&Run{
			CommitHash: fmt.Sprintf("commit-%d", i),
			Branch:     "main",
			RunDate:    runDate,
		})
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, runID)
		if i != 1 {
			insertTestResult(t, database, runID, "render", fmt.Sprintf("frame-%d", i))
		}
	}

	targets := []int64{runIDs[3], runIDs[0], runIDs[2], runIDs[2], runIDs[1]}
	batched, err := database.RegressionDataFingerprints(targets)
	if err != nil {
		t.Fatalf("batch fingerprints: %v", err)
	}
	if len(batched) != len(runIDs) {
		t.Fatalf("batch returned %d fingerprints, want %d", len(batched), len(runIDs))
	}
	for _, runID := range runIDs {
		individual, err := database.RegressionDataFingerprint(runID)
		if err != nil {
			t.Fatalf("individual fingerprint for run %d: %v", runID, err)
		}
		if batched[runID] != individual {
			t.Errorf("run %d batch fingerprint = %q, want %q", runID, batched[runID], individual)
		}
	}
}

func TestOpenDropsObsoleteDFKeyedRegressionCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-cache.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE runs (
			id INTEGER PRIMARY KEY, commit_hash TEXT NOT NULL, commit_hash_full TEXT,
			commit_message TEXT, commit_date TEXT, branch TEXT, run_date TEXT NOT NULL,
			machine_id TEXT, notes TEXT, zig_optimize TEXT DEFAULT 'ReleaseFast'
		);
		CREATE TABLE regression_cache (
			id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL, branch TEXT NOT NULL,
			window INTEGER NOT NULL, min_points INTEGER NOT NULL, baseline_offset INTEGER NOT NULL,
			df_mode TEXT NOT NULL, generation_key TEXT NOT NULL, response_json TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(run_id, branch, window, min_points, baseline_offset, df_mode)
		);
		INSERT INTO runs(id, commit_hash, run_date) VALUES (1, 'old', '2026-01-01T00:00:00Z');
		INSERT INTO regression_cache VALUES (1, 1, 'main', 30, 5, 3, 'baseline', 'old', '{}', 'now', 'now');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	rows, err := database.Query(`PRAGMA table_info(regression_cache)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "df_mode" {
			t.Fatal("obsolete df_mode cache column remains reachable")
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM regression_cache`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("obsolete cache rows remain: %d", count)
	}
}

func TestCalibrationRolloutMigrationPurgesRegressionCacheOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-cache.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO runs(id, commit_hash, run_date) VALUES (1, 'old', '2026-01-01T00:00:00Z');
		INSERT INTO regression_cache(run_id, branch, window, min_points, baseline_offset, generation_key, response_json, created_at, updated_at)
		VALUES (1, 'main', 30, 5, 3, 'old-generation', '{}', 'now', 'now');
		PRAGMA user_version = 4;
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var count, version int
	if err := database.QueryRow(`SELECT COUNT(*) FROM regression_cache`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if count != 0 || version != CurrentSchemaVersion {
		t.Fatalf("count=%d version=%d, want empty cache at version %d", count, version, CurrentSchemaVersion)
	}
	if _, err := database.Exec(`INSERT INTO regression_cache(run_id, branch, window, min_points, baseline_offset, generation_key, response_json, created_at, updated_at)
		VALUES (1, 'main', 30, 5, 3, 'new-generation', '{}', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM regression_cache`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("versioned purge reran and removed current cache: count=%d", count)
	}
}

func TestMigrationPreservesHistoricalResultsWithoutFabricatingSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "production.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE runs (id INTEGER PRIMARY KEY, commit_hash TEXT NOT NULL, commit_hash_full TEXT,
			commit_message TEXT, commit_date TEXT, branch TEXT, run_date TEXT NOT NULL,
			machine_id TEXT, notes TEXT, zig_optimize TEXT DEFAULT 'ReleaseFast');
		CREATE TABLE results (id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL REFERENCES runs(id),
			category TEXT NOT NULL, name TEXT NOT NULL, min_ns INTEGER NOT NULL, avg_ns INTEGER NOT NULL,
			max_ns INTEGER NOT NULL, std_dev_ns INTEGER NOT NULL DEFAULT 0, p50_ns INTEGER NOT NULL DEFAULT 0,
			p95_ns INTEGER NOT NULL DEFAULT 0, p99_ns INTEGER NOT NULL DEFAULT 0, total_ns INTEGER NOT NULL,
			iterations INTEGER NOT NULL, sample_count INTEGER NOT NULL DEFAULT 1);
		CREATE TABLE jobs (id INTEGER PRIMARY KEY, status TEXT NOT NULL DEFAULT 'pending',
			kind TEXT NOT NULL DEFAULT 'benchmark', branch TEXT NOT NULL, commit_hash TEXT,
			repo_url TEXT NOT NULL DEFAULT 'origin', samples INTEGER NOT NULL DEFAULT 3,
			profile TEXT NOT NULL DEFAULT 'cpu', notes TEXT, created_at TEXT NOT NULL,
			started_at TEXT, completed_at TEXT, error TEXT, run_id INTEGER REFERENCES runs(id),
			requested_by TEXT);
		CREATE VIEW results_with_run AS
			SELECT r.id AS result_id, r.avg_ns, ru.id AS run_id, ru.commit_hash
			FROM results r JOIN runs ru ON r.run_id = ru.id;
		INSERT INTO runs VALUES (1, 'abc', 'abcdef', '', '', 'main', '2026-01-01T00:00:00Z', 'host', '', 'ReleaseFast');
		INSERT INTO results VALUES (1, 1, 'cat', 'bench', 1, 2, 3, 1, 2, 3, 3, 10, 5, 3);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var version int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("version = %d, err = %v", version, err)
	}
	result, err := database.GetResult(1)
	if err != nil {
		t.Fatal(err)
	}
	if result.SampleAvgVarianceNs2 != nil || result.SampleDataVersion != 0 || result.SummaryVersion != 1 || len(result.Samples) != 0 {
		t.Fatalf("historical provenance changed: %+v", result)
	}
	viewColumns, err := database.Query(`PRAGMA table_info(results_with_run)`)
	if err != nil {
		t.Fatal(err)
	}
	foundVariance := false
	for viewColumns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := viewColumns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "sample_avg_variance_ns2" {
			foundVariance = true
		}
	}
	if !foundVariance {
		t.Fatal("migrated results_with_run view is missing precision columns")
	}
	if err := viewColumns.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}

func TestMigrationCompressesLegacyFlamegraphs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-flamegraph.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE runs (id INTEGER PRIMARY KEY, commit_hash TEXT NOT NULL, commit_hash_full TEXT,
			commit_message TEXT, commit_date TEXT, branch TEXT, run_date TEXT NOT NULL,
			machine_id TEXT, notes TEXT, zig_optimize TEXT DEFAULT 'ReleaseFast');
		CREATE TABLE flamegraphs (id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL, benchmark_name TEXT NOT NULL,
			folded_stacks TEXT NOT NULL, svg TEXT, sampling_freq INTEGER NOT NULL DEFAULT 997, created_at TEXT NOT NULL);
		INSERT INTO runs VALUES (1, 'abc', '', '', '', 'main', '2026-01-01T00:00:00Z', '', '', 'ReleaseFast');
		INSERT INTO flamegraphs VALUES (1, 1, 'bench', 'main;work 7', '<svg/>', 997, 'now');
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	flamegraph, err := database.GetFlamegraph(1, "bench")
	if err != nil {
		t.Fatal(err)
	}
	if flamegraph.FoldedStacks != "main;work 7" {
		t.Fatalf("folded stacks = %q", flamegraph.FoldedStacks)
	}
}

func TestMigrationFailureRollsBackVersionAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE results (id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL, category TEXT NOT NULL, name TEXT NOT NULL);
		INSERT INTO results VALUES (1, 1, 'cat', 'same'), (2, 1, 'cat', 'same');
		PRAGMA user_version = 1;
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if database, err := Open(path); err == nil {
		_ = database.Close()
		t.Fatal("expected duplicate identity migration failure")
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var version, rows int
	_ = raw.QueryRow(`PRAGMA user_version`).Scan(&version)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&rows)
	if version != 1 || rows != 2 {
		t.Fatalf("migration partially applied: version=%d rows=%d", version, rows)
	}
}

func TestOpenRejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if database, err := Open(path); err == nil {
		_ = database.Close()
		t.Fatal("expected newer schema rejection")
	}
}

func TestOpenReadOnlyRejectsOlderSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-read-only.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, CurrentSchemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if database, err := OpenReadOnly(path); err == nil {
		_ = database.Close()
		t.Fatal("expected older read-only schema rejection")
	} else if !strings.Contains(err.Error(), "older") || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("error = %q, want clear migration guidance", err)
	}
}

func TestInsertRunWithResultsRollsBackOnSampleConstraint(t *testing.T) {
	database := openTestDB(t)
	_, _, err := database.InsertRunWithResults(&Run{CommitHash: "bad", RunDate: "2026-01-01T00:00:00Z"}, []Result{{
		Category: "cat", Name: "bench", MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 2, Iterations: 1,
		SampleCount: 1, SampleDataVersion: 1, SummaryVersion: 2,
		Samples: []ResultSample{{SampleIndex: 0, AvgNs: 0}},
	}})
	if err == nil {
		t.Fatal("expected sample CHECK failure")
	}
	var runs, results int
	_ = database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs)
	_ = database.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&results)
	if runs != 0 || results != 0 {
		t.Fatalf("partial write remains: runs=%d results=%d", runs, results)
	}
}

func TestJavaScriptRunRoundTripRetainsIdentityAndBatchEvidence(t *testing.T) {
	database := openTestDB(t)
	rsd := int64(1234)
	run := &Run{
		CommitHash: "abc", CommitHashFull: "abcdef", Branch: "main", RunDate: "2026-08-04T00:00:00Z",
		MachineID: "runner", BenchmarkKind: "js", BenchmarkSuite: "core-default", ProtocolVersion: 1,
		BunVersion: "1.3.14", ZigVersion: "0.15.2", ManifestHash: "sha256:test", ManifestJSON: `{"hash":"sha256:test"}`,
	}
	runID, ids, err := database.InsertRunWithResults(run, []Result{{
		Category: "JS Layout", Name: "leaf", MinNs: 10, AvgNs: 11, MaxNs: 12,
		TotalNs: 22, Iterations: 2, SampleCount: 1, Samples: []ResultSample{{
			SampleIndex: 0, AvgNs: 11, InnerRSDPPM: &rsd,
			Batches: []ResultSampleBatch{{BatchIndex: 0, ElapsedNs: 10, Iterations: 1}, {BatchIndex: 1, ElapsedNs: 12, Iterations: 1}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BenchmarkKind != "js" || stored.BunVersion != "1.3.14" || stored.ManifestJSON != run.ManifestJSON {
		t.Fatalf("stored run = %+v", stored)
	}
	result, err := database.GetResult(ids[BenchmarkKey{Category: "JS Layout", Name: "leaf"}])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 1 || result.Samples[0].InnerRSDPPM == nil || *result.Samples[0].InnerRSDPPM != rsd || len(result.Samples[0].Batches) != 2 {
		t.Fatalf("stored evidence = %+v", result.Samples)
	}
}

func TestZigSampleRoundTripKeepsInnerRSDNull(t *testing.T) {
	database := openTestDB(t)
	runID, ids, err := database.InsertRunWithResults(&Run{CommitHash: "zig", RunDate: "2026-08-04T00:00:00Z"}, []Result{{
		Category: "render", Name: "frame", MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 2,
		Iterations: 1, SampleCount: 1, Samples: []ResultSample{{SampleIndex: 0, AvgNs: 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	storedRun, err := database.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.BenchmarkKind != "zig" || storedRun.BenchmarkSuite != "core-default" || storedRun.ProtocolVersion != 1 {
		t.Fatalf("Zig defaults = %+v", storedRun)
	}
	result, err := database.GetResult(ids[BenchmarkKey{Category: "render", Name: "frame"}])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 1 || result.Samples[0].InnerRSDPPM != nil || len(result.Samples[0].Batches) != 0 {
		t.Fatalf("Zig sample evidence = %+v", result.Samples)
	}
}

func TestRegularRunInsertsPreserveDuplicateMeasurementIdentities(t *testing.T) {
	database := openTestDB(t)
	base := Run{CommitHash: "abc", CommitHashFull: "abcdef", Branch: "", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner"}
	if _, err := database.InsertRun(&base); err != nil {
		t.Fatal(err)
	}
	duplicate := base
	duplicate.Branch = "main"
	if _, err := database.InsertRun(&duplicate); err != nil {
		t.Fatalf("historical/local duplicate rejected: %v", err)
	}
	javascript := base
	javascript.BenchmarkKind = "js"
	javascript.BunVersion = "1.3.14"
	javascript.ZigVersion = "0.15.2"
	javascript.ManifestHash = "sha256:test"
	if _, err := database.InsertRun(&javascript); err != nil {
		t.Fatalf("separate JavaScript identity rejected: %v", err)
	}
}

func TestRemoteIdempotencyUsesFullMeasurementIdentity(t *testing.T) {
	database := openTestDB(t)
	results := []Result{{Category: "render", Name: "frame", MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 2, Iterations: 1, SampleCount: 1}}
	base := Run{CommitHash: "abc", CommitHashFull: "abcdef", Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner"}
	zig, _, created, err := database.InsertRunWithResultsIfAbsent(&base, results)
	if err != nil || !created || zig == nil {
		t.Fatalf("insert Zig identity: created=%v err=%v", created, err)
	}
	normalizedBranch := base
	normalizedBranch.Branch = ""
	same, _, created, err := database.InsertRunWithResultsIfAbsent(&normalizedBranch, results)
	if err != nil || created || same == nil || same.ID != zig.ID {
		t.Fatalf("normalized branch identity: run=%+v created=%v err=%v", same, created, err)
	}
	javascript := base
	javascript.BenchmarkKind = "js"
	javascript.BunVersion = "1.3.14"
	javascript.ZigVersion = "0.15.2"
	javascript.ManifestHash = "sha256:test"
	js, _, created, err := database.InsertRunWithResultsIfAbsent(&javascript, results)
	if err != nil || !created || js == nil || js.ID == zig.ID {
		t.Fatalf("insert distinct JavaScript identity: run=%+v created=%v err=%v", js, created, err)
	}
}

func TestVersion5MigrationPreservesDuplicateRunsForRemoteIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-5-duplicates.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		DROP INDEX idx_runs_idempotency_key;
		ALTER TABLE runs DROP COLUMN idempotency_key;
		INSERT INTO runs(commit_hash, commit_hash_full, branch, run_date, machine_id)
		VALUES ('abc', 'abcdef', 'main', '2026-08-01T00:00:00Z', 'runner'),
		       ('abc', 'abcdef', 'main', '2026-08-02T00:00:00Z', 'runner');
		PRAGMA user_version = 5;
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(path)
	if err != nil {
		t.Fatalf("migrate duplicate version 5 runs: %v", err)
	}
	defer func() { _ = database.Close() }()
	var historical, keyed int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs WHERE idempotency_key IS NULL`).Scan(&historical); err != nil {
		t.Fatal(err)
	}
	if historical != 2 {
		t.Fatalf("historical runs = %d, want 2", historical)
	}

	run := Run{CommitHash: "abc", CommitHashFull: "abcdef", Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner"}
	results := []Result{{Category: "render", Name: "frame", MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 2, Iterations: 1, SampleCount: 1}}
	first, _, created, err := database.InsertRunWithResultsIfAbsent(&run, results)
	if err != nil || !created {
		t.Fatalf("first remote insertion: created=%v err=%v", created, err)
	}
	second, _, created, err := database.InsertRunWithResultsIfAbsent(&run, results)
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("repeated remote insertion: first=%d second=%d created=%v err=%v", first.ID, second.ID, created, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs WHERE idempotency_key IS NOT NULL`).Scan(&keyed); err != nil {
		t.Fatal(err)
	}
	if keyed != 1 {
		t.Fatalf("idempotent runs = %d, want 1", keyed)
	}
}

func TestVersion8MigrationBackfillsJavaScriptRuntimeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-7-runtime.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO runs(commit_hash, run_date, benchmark_kind, bun_version, js_runtime, runtime_version)
		VALUES ('legacy', '2026-08-04T00:00:00Z', 'js', '1.2.3', '', '');
		INSERT INTO jobs(status, kind, branch, samples, profile, created_at, benchmark_kind, benchmark_suite,
			protocol_version, manifest_hash, js_runtime, runtime_version)
		VALUES ('pending', 'benchmark', 'main', 3, 'none', '2026-08-04T00:00:00Z', 'js', 'core-default',
			1, ?, '', '');
		PRAGMA user_version = 7`, jsbench.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var runRuntime, runVersion, jobRuntime, jobVersion string
	if err := database.QueryRow(`SELECT js_runtime, runtime_version FROM runs WHERE commit_hash = 'legacy'`).Scan(&runRuntime, &runVersion); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT js_runtime, runtime_version FROM jobs`).Scan(&jobRuntime, &jobVersion); err != nil {
		t.Fatal(err)
	}
	if runRuntime != jsbench.RuntimeBun || runVersion != "1.2.3" || jobRuntime != jsbench.RuntimeBun || jobVersion != jsbench.BunVersion {
		t.Fatalf("run=%s/%s job=%s/%s", runRuntime, runVersion, jobRuntime, jobVersion)
	}
}

func TestRuntimeIdentitySeparatesCohortsAndClaims(t *testing.T) {
	database := openTestDB(t)
	bun := &Run{BenchmarkKind: jsbench.Kind, BenchmarkSuite: jsbench.Suite, ProtocolVersion: jsbench.Protocol,
		JSRuntime: jsbench.RuntimeBun, RuntimeVersion: jsbench.BunVersion, BunVersion: jsbench.BunVersion,
		ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest, MachineID: "runner"}
	node := *bun
	node.JSRuntime, node.RuntimeVersion, node.BunVersion = jsbench.RuntimeNode, jsbench.NodeVersion, ""
	if SameRunCohort(bun, &node) || !CrossRuntimeCompatible(bun, &node) {
		t.Fatalf("bun/node compatibility: same=%v cross=%v", SameRunCohort(bun, &node), CrossRuntimeCompatible(bun, &node))
	}
	jobID, err := database.InsertJob(&Job{Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3,
		Profile: "none", CreatedAt: "2026-08-04T00:00:00Z", BenchmarkKind: jsbench.Kind,
		BenchmarkSuite: jsbench.Suite, ProtocolVersion: jsbench.Protocol, ManifestHash: jsbench.ManifestDigest,
		JSRuntime: jsbench.RuntimeNode, RuntimeVersion: jsbench.NodeVersion})
	if err != nil {
		t.Fatal(err)
	}
	token, err := joblease.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingJobWithToken(jsbench.Kind, token)
	if err != nil || claimed != nil {
		t.Fatalf("Bun-compatible claim took Node job: job=%+v err=%v", claimed, err)
	}
	token, _ = joblease.NewToken()
	claimed, err = database.ClaimNextPendingJobWithToken(jsbench.Kind, token, jsbench.RuntimeNode)
	if err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatalf("Node claim: job=%+v err=%v", claimed, err)
	}
}

func TestVersion6ClaimTokenMigrationLeavesRunningRowsLeased(t *testing.T) {
	for _, test := range []struct {
		name       string
		dropColumn bool
	}{
		{name: "existing claim column"},
		{name: "missing claim column", dropColumn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "version-6.db")
			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := database.InsertJob(&Job{
				Status: "running", Kind: "benchmark", Branch: "main", Samples: 3,
				Profile: "cpu", CreatedAt: timeNow(),
			})
			if err != nil {
				t.Fatal(err)
			}
			reclaimID, err := database.InsertJob(&Job{
				Status: "running", Kind: "benchmark", Branch: "main", Samples: 3,
				Profile: "cpu", CreatedAt: timeNow(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE jobs SET started_at = ?, claim_token = 'preexisting-' || id WHERE id IN (?, ?)`,
				timeNow(), jobID, reclaimID); err != nil {
				t.Fatal(err)
			}
			if test.dropColumn {
				if _, err := database.Exec(`DROP INDEX idx_jobs_claim_token; ALTER TABLE jobs DROP COLUMN claim_token; ALTER TABLE jobs DROP COLUMN legacy_tokenless`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.Exec(`PRAGMA user_version = 6`); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			database, err = Open(path)
			if err != nil {
				t.Fatalf("migrate v6 database: %v", err)
			}
			defer func() { _ = database.Close() }()
			job, err := database.GetJob(jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "running" || job.StartedAt == "" || job.ClaimToken != "" {
				t.Fatalf("migrated running job = %+v, want active job without token material", job)
			}
			claimed, err := database.ClaimNextPendingJob("")
			if err != nil {
				t.Fatal(err)
			}
			if claimed != nil {
				t.Fatalf("migration immediately requeued active job: %+v", claimed)
			}
			if err := database.UpdateJobCommitHash(context.Background(), jobID, "", "legacy-commit"); err != nil {
				t.Fatalf("migrated worker commit update: %v", err)
			}
			runID, err := database.InsertRun(&Run{
				CommitHash: "legacy-commit", CommitHashFull: "legacy-commit", RunDate: timeNow(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteJob(context.Background(), jobID, "", runID); err != nil {
				t.Fatalf("migrated worker completion: %v", err)
			}
			if _, err := database.Exec(`UPDATE jobs SET started_at = ? WHERE id = ?`,
				time.Now().UTC().Add(-JobLeaseDuration-time.Hour).Format(time.RFC3339), reclaimID); err != nil {
				t.Fatal(err)
			}
			claimed, err = database.ClaimNextPendingJob("")
			if err != nil || claimed == nil || claimed.ID != reclaimID {
				t.Fatalf("stale migrated job claim = %+v, err=%v", claimed, err)
			}
			if err := database.FailJob(context.Background(), reclaimID, "", "legacy worker"); !errors.Is(err, ErrJobClaimLost) {
				t.Fatalf("tokenless mutation after migrated job reclaim = %v, want ErrJobClaimLost", err)
			}
		})
	}
}

func TestConcurrentIdenticalRunSubmissionsCreateOneCompleteRun(t *testing.T) {
	database := openTestDB(t)
	concurrent, err := Open(database.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = concurrent.Close() })
	base := Run{CommitHash: "abc", CommitHashFull: "abcdef", Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner"}
	results := []Result{{Category: "render", Name: "frame", MinNs: 1, AvgNs: 2, MaxNs: 3, TotalNs: 2, Iterations: 1, SampleCount: 1}}
	type outcome struct {
		id      int64
		created bool
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, connection := range []*DB{database, concurrent} {
		go func(connection *DB) {
			run := base
			ready.Done()
			<-start
			stored, _, created, err := connection.InsertRunWithResultsIfAbsent(&run, results)
			var id int64
			if stored != nil {
				id = stored.ID
			}
			outcomes <- outcome{id: id, created: created, err: err}
		}(connection)
	}
	ready.Wait()
	close(start)
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil || first.id == 0 || first.id != second.id || first.created == second.created {
		t.Fatalf("outcomes = %+v, %+v", first, second)
	}
	var runs, storedResults int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&storedResults); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || storedResults != 1 {
		t.Fatalf("stored rows = %d runs, %d results", runs, storedResults)
	}
}

func TestJavaScriptComparableRunsRequireCompleteIdentityMatch(t *testing.T) {
	database := openTestDB(t)
	insert := func(date, kind, suite, bun, zig string, protocol int64, manifest string) (int64, int64) {
		t.Helper()
		id, err := database.InsertRun(&Run{CommitHash: date, Branch: "main", RunDate: date, MachineID: "runner",
			BenchmarkKind: kind, BenchmarkSuite: suite, ProtocolVersion: protocol, BunVersion: bun,
			ZigVersion: zig, ManifestHash: manifest})
		if err != nil {
			t.Fatal(err)
		}
		return id, insertTestResult(t, database, id, "JS Layout", "leaf")
	}
	want, wantResult := insert("2026-08-01T00:00:00Z", "js", "core-default", "1.3.14", "0.15.2", 1, "sha256:a")
	_, _ = insert("2026-08-01T01:00:00Z", "zig", "core-default", "", "", 1, "")
	_, _ = insert("2026-08-02T00:00:00Z", "js", "other", "1.3.14", "0.15.2", 1, "sha256:a")
	_, _ = insert("2026-08-02T01:00:00Z", "js", "core-default", "1.3.14", "0.15.2", 2, "sha256:a")
	_, _ = insert("2026-08-02T02:00:00Z", "js", "core-default", "1.3.14", "0.15.3", 1, "sha256:a")
	_, _ = insert("2026-08-02T03:00:00Z", "js", "core-default", "1.3.14", "0.15.2", 1, "sha256:b")
	_, _ = insert("2026-08-03T00:00:00Z", "js", "core-default", "1.3.15", "0.15.2", 1, "sha256:a")
	reference, referenceResult := insert("2026-08-04T00:00:00Z", "js", "core-default", "1.3.14", "0.15.2", 1, "sha256:a")
	runs, err := database.GetComparableRunsWindow(reference, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != reference || runs[1].ID != want {
		t.Fatalf("cohort = %+v, want [%d %d]", runs, reference, want)
	}
	trend, err := database.GetTrend(referenceResult, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 2 || trend[0].Result.ID != referenceResult || trend[1].Result.ID != wantResult {
		t.Fatalf("trend = %+v, want result IDs [%d %d]", trend, referenceResult, wantResult)
	}
}

func TestSameRunCohortUsesMachineAndKindSpecificIdentity(t *testing.T) {
	base := &Run{
		MachineID: "runner", BenchmarkKind: "js", BenchmarkSuite: "core-default", ProtocolVersion: 1,
		BunVersion: "1.3.14", ZigVersion: "0.15.2", ManifestHash: "sha256:a", ZigOptimize: "ignored",
	}
	same := *base
	same.ZigOptimize = "also-ignored"
	if !SameRunCohort(base, &same) {
		t.Fatal("JavaScript cohort treated Zig optimization as identity")
	}

	for name, mutate := range map[string]func(*Run){
		"machine":  func(run *Run) { run.MachineID = "other" },
		"kind":     func(run *Run) { run.BenchmarkKind = "zig" },
		"suite":    func(run *Run) { run.BenchmarkSuite = "other" },
		"protocol": func(run *Run) { run.ProtocolVersion++ },
		"bun":      func(run *Run) { run.BunVersion = "1.3.15" },
		"zig":      func(run *Run) { run.ZigVersion = "0.15.3" },
		"manifest": func(run *Run) { run.ManifestHash = "sha256:b" },
	} {
		t.Run(name, func(t *testing.T) {
			other := *base
			mutate(&other)
			if SameRunCohort(base, &other) {
				t.Fatalf("different %s accepted as same cohort", name)
			}
		})
	}

	zig := &Run{MachineID: "runner", BenchmarkKind: "zig", BenchmarkSuite: "core-default", ProtocolVersion: 1, ZigOptimize: "ReleaseFast"}
	otherOptimize := *zig
	otherOptimize.ZigOptimize = "ReleaseSafe"
	if SameRunCohort(zig, &otherOptimize) {
		t.Fatal("different Zig optimization accepted as same cohort")
	}
}

func TestFailJobBoundsPersistedError(t *testing.T) {
	database := openTestDB(t)
	jobID, err := database.InsertJob(&Job{Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "none", CreatedAt: timeNow()})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingJob("")
	if err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatalf("claim job: job=%+v err=%v", claimed, err)
	}
	if err := database.FailJob(context.Background(), jobID, claimed.ClaimToken, strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	job, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Error) != 4096 {
		t.Fatalf("stored error length = %d, want 4096", len(job.Error))
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
