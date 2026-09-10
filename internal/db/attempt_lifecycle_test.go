package db

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func claimAttemptJob(t *testing.T, database *DB, a *InvestigationAttempt) *Job {
	t.Helper()
	job, err := database.ClaimNextPendingJobIncludingInvestigation("zig", strings.Repeat("a", 64))
	if err != nil || job == nil || job.ID != a.JobID {
		t.Fatalf("claim attempt job: %+v %v", job, err)
	}
	return job
}

func TestInvestigationJobTransitionsUpdateAttempt(t *testing.T) {
	database := openTestDB(t)
	_, a := createTestInvestigation(t, database, "lifecycle")
	assertStatus := func(status string) {
		t.Helper()
		job, err := database.GetJob(a.JobID)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := database.GetAttemptByKey(a.AttemptKey)
		if err != nil || job.Status != status || attempt.Status != status || attempt.Error != job.Error {
			t.Fatalf("want %s: job=%+v attempt=%+v error=%v", status, job, attempt, err)
		}
	}
	assertStatus("pending")
	job := claimAttemptJob(t, database, a)
	assertStatus("running")
	claimAttemptJob(t, database, a)
	assertStatus("running")
	if err := database.ReleaseJob(context.Background(), job.ID, job.ClaimToken); err != nil {
		t.Fatal(err)
	}
	assertStatus("pending")
	job = claimAttemptJob(t, database, a)
	if _, err := database.Exec(`UPDATE jobs SET started_at = '2000-01-01T00:00:00Z' WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := database.ClaimNextPendingJob("zig"); err != nil || claimed != nil {
		t.Fatalf("ordinary worker reclaimed investigation: %+v %v", claimed, err)
	}
	assertStatus("pending")
	job = claimAttemptJob(t, database, a)
	if err := database.FailJob(context.Background(), job.ID, job.ClaimToken, "capture failed"); err != nil {
		t.Fatal(err)
	}
	assertStatus("failed")
}

func TestTerminalParentJobRejectsNewRecording(t *testing.T) {
	for _, status := range []string{"cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			database := openTestDB(t)
			inv, a := createTestInvestigation(t, database, "terminal-parent")
			runs, recipe := attemptPayload(t, inv, a)
			if status == "cancelled" {
				if err := database.CancelJob(a.JobID); err != nil {
					t.Fatal(err)
				}
			} else {
				job := claimAttemptJob(t, database, a)
				if err := database.FailJob(context.Background(), job.ID, job.ClaimToken, "build failed"); err != nil {
					t.Fatal(err)
				}
			}
			attempt, err := database.GetAttemptByKey(a.AttemptKey)
			if err != nil || attempt.Status != status {
				t.Fatalf("terminal attempt: %+v %v", attempt, err)
			}
			if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err == nil {
				t.Fatal("terminal attempt accepted a new snapshot")
			}
			if _, err := database.Exec(`UPDATE investigation_attempts SET status = 'pending' WHERE id = ?`, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err == nil || !strings.Contains(err.Error(), "job is "+status) {
				t.Fatalf("legacy pending child bypassed terminal parent: %v", err)
			}
			var count int
			if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("terminal parent retained %d new runs: %v", count, err)
			}
		})
	}
}

func TestInvestigationJobTransitionsRollBackTogether(t *testing.T) {
	for _, transition := range []string{"claim", "release", "reclaim", "fail", "cancel"} {
		t.Run(transition, func(t *testing.T) {
			database := openTestDB(t)
			_, a := createTestInvestigation(t, database, "rollback")
			if transition != "claim" && transition != "cancel" {
				claimAttemptJob(t, database, a)
			}
			if transition == "reclaim" {
				if _, err := database.Exec(`UPDATE jobs SET started_at = '2000-01-01T00:00:00Z' WHERE id = ?`, a.JobID); err != nil {
					t.Fatal(err)
				}
			}
			beforeJob, err := database.GetJob(a.JobID)
			if err != nil {
				t.Fatal(err)
			}
			beforeAttempt, err := database.GetAttemptByKey(a.AttemptKey)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER interrupt_transition BEFORE UPDATE OF status ON investigation_attempts
				BEGIN SELECT RAISE(ABORT, 'interrupted transition'); END`); err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("a", 64)
			switch transition {
			case "claim":
				_, err = database.ClaimNextPendingJobIncludingInvestigation("zig", token)
			case "release":
				err = database.ReleaseJob(context.Background(), a.JobID, token)
			case "reclaim":
				_, err = database.ClaimNextPendingJob("zig")
			case "fail":
				err = database.FailJob(context.Background(), a.JobID, token, "capture failed")
			case "cancel":
				err = database.CancelJob(a.JobID)
			}
			if err == nil || !strings.Contains(err.Error(), "interrupted transition") {
				t.Fatalf("interrupted transition did not fail: %v", err)
			}
			afterJob, err := database.GetJob(a.JobID)
			if err != nil || !reflect.DeepEqual(beforeJob, afterJob) {
				t.Fatalf("partial job transition survived: %+v %v", afterJob, err)
			}
			afterAttempt, err := database.GetAttemptByKey(a.AttemptKey)
			if err != nil || !reflect.DeepEqual(beforeAttempt, afterAttempt) {
				t.Fatalf("partial attempt transition survived: %+v %v", afterAttempt, err)
			}
		})
	}
}

func TestCompletedSnapshotSurvivesJobLifecycleRetries(t *testing.T) {
	for _, terminal := range []string{"cancelled", "failed", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			database := openTestDB(t)
			inv, a := createTestInvestigation(t, database, "completed-snapshot")
			job := claimAttemptJob(t, database, a)
			runs, recipe := attemptPayload(t, inv, a)
			stored, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE jobs SET started_at = '2000-01-01T00:00:00Z' WHERE id = ?`, job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ClaimNextPendingJob("zig"); err != nil {
				t.Fatal(err)
			}
			job = claimAttemptJob(t, database, a)
			if err := database.ReleaseJob(context.Background(), job.ID, job.ClaimToken); err != nil {
				t.Fatal(err)
			}
			if terminal == "cancelled" {
				err = database.CancelJob(job.ID)
			} else {
				job = claimAttemptJob(t, database, a)
				if terminal == "failed" {
					err = database.FailJob(context.Background(), job.ID, job.ClaimToken, "response lost")
				} else {
					err = database.CompleteJob(context.Background(), job.ID, job.ClaimToken, stored.RunID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			retry, created, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
			if err != nil || created || !reflect.DeepEqual(stored, retry) {
				t.Fatalf("%s parent changed completed snapshot: %+v %v", terminal, retry, err)
			}
		})
	}
}
