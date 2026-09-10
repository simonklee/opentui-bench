package db

import (
	"testing"
)

func investigationResult(category, name string, avgNs int64) Result {
	return Result{
		Category: category, Name: name,
		MinNs: avgNs, AvgNs: avgNs, MaxNs: avgNs, P50Ns: avgNs, P95Ns: avgNs, P99Ns: avgNs,
		TotalNs: avgNs, Iterations: 1, SampleCount: 1,
	}
}

func TestInvestigationAttemptsAreIndependentAndIdempotent(t *testing.T) {
	database := openTestDB(t)
	triggerRunID, err := database.InsertRun(&Run{
		CommitHash: "target", CommitHashFull: "targetfull", Branch: "main",
		RunDate: "2026-09-07T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast",
	})
	if err != nil {
		t.Fatal(err)
	}
	triggerID := insertTestResult(t, database, triggerRunID, "Sixel", "160x240 flat")

	first, attemptA, _, created, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		TriggerResultID: triggerID, Category: "Sixel", Name: "160x240 flat",
		BaselineCommit: "basefull", TargetCommit: "targetfull",
		AttemptKey: "pair-1", Branch: "main", Samples: 3, Profile: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.ID == 0 {
		t.Fatalf("created=%v investigation=%+v", created, first)
	}

	same, _, _, created, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		TriggerResultID: triggerID, Category: "Sixel", Name: "160x240 flat",
		BaselineCommit: "basefull", TargetCommit: "targetfull",
		AttemptKey: "pair-2", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || same.ID != first.ID {
		t.Fatalf("duplicate identity created=%v id=%d want %d", created, same.ID, first.ID)
	}

	attemptB, _, created, err := database.CreatePairAttempt(first.ID, InvestigationCreate{AttemptKey: "pair-2", Profile: "none"})
	if err != nil || !created {
		t.Fatalf("create fresh pair: created=%v err=%v", created, err)
	}
	runsA, recipeA := attemptPayload(t, first, attemptA)
	storedA, created, err := database.RecordAttempt(attemptA.AttemptKey, recipeA, runsA, testAttemptRetention())
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first attempt recording was treated as a duplicate")
	}

	runsB, recipeB := attemptPayload(t, first, attemptB)
	runsB[1].Results[0].AvgNs = 110
	storedB, created, err := database.RecordAttempt(attemptB.AttemptKey, recipeB, runsB, testAttemptRetention())
	if err != nil {
		t.Fatal(err)
	}
	if !created || storedB.TargetRunID == storedA.TargetRunID || storedB.BaselineRunID == storedA.BaselineRunID {
		t.Fatalf("second attempt reused runs: %+v", storedB)
	}

	runsA[1].Results[0].AvgNs = 999
	retried, created, err := database.RecordAttempt(attemptA.AttemptKey, recipeA, runsA, testAttemptRetention())
	if err != nil {
		t.Fatal(err)
	}
	if created || retried.ID != storedA.ID {
		t.Fatalf("retry created=%v id=%d want %d", created, retried.ID, storedA.ID)
	}
	results, err := database.GetResultsForRun(storedA.TargetRunID)
	if err != nil || len(results) != 1 || results[0].AvgNs != 100 {
		t.Fatalf("original results were replaced: %+v, %v", results, err)
	}

	latest, err := database.GetLatestRunFiltered("main", RunFilter{BenchmarkKind: "zig"})
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != triggerRunID {
		t.Fatalf("latest history run = %d, want original %d", latest.ID, triggerRunID)
	}
	exists, err := database.HasCommitFiltered("targetfull", RunFilter{BenchmarkKind: "zig"})
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("history commit should still be visible")
	}
}

func TestInvestigationRecordingDoesNotAdvanceSchedulerCursor(t *testing.T) {
	database := openTestDB(t)
	historyID, err := database.InsertRun(&Run{
		CommitHash: "old", CommitHashFull: "oldfull", Branch: "main",
		RunDate: "2026-01-01T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast",
	})
	if err != nil {
		t.Fatal(err)
	}
	inv, attempt, _, _, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "cat", Name: "bench", BaselineCommit: "oldfull", TargetCommit: "replayedfull",
		AttemptKey: "replay-1", Profile: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, recipe := attemptPayload(t, inv, attempt)
	if _, _, err := database.RecordAttempt(attempt.AttemptKey, recipe, runs, testAttemptRetention()); err != nil {
		t.Fatal(err)
	}

	latest, err := database.GetLatestRunFiltered("main", RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != historyID {
		t.Fatalf("latest run = %d (%s), want history %d", latest.ID, latest.CommitHash, historyID)
	}
	exists, err := database.HasCommitFiltered("replayedfull", RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("investigation replay was treated as recorded history")
	}
}

func TestCreateCandidateAttemptReusesAttemptKey(t *testing.T) {
	database := openTestDB(t)
	inv, _, _, created, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "cat", Name: "bench", BaselineCommit: "base", TargetCommit: "target",
		AttemptKey: "pair-key",
	})
	if err != nil || !created {
		t.Fatalf("create investigation: created=%v err=%v", created, err)
	}
	first, job, created, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{
		BenchmarkKind: "zig", Category: "cat", Name: "bench",
		TargetCommit: "fix", AttemptKey: "cand-1", Branch: "fix-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || job == nil || first.Role != AttemptRoleCandidate {
		t.Fatalf("created=%v job=%v attempt=%+v", created, job, first)
	}
	second, job2, created, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{
		BenchmarkKind: "zig", Category: "cat", Name: "bench",
		TargetCommit: "fix", AttemptKey: "cand-1", Branch: "fix-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || second.ID != first.ID || job2.ID != job.ID {
		t.Fatalf("retry created=%v attempt=%d job=%d", created, second.ID, job2.ID)
	}
	if job.BaselineCommit != inv.BaselineCommit || job.ComparisonCommit != inv.TargetCommit ||
		job.Category != inv.Category || job.Name != inv.Name || job.BenchmarkKind != inv.BenchmarkKind {
		t.Fatalf("candidate job does not derive investigation identity: %+v", job)
	}
	if _, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{
		TargetCommit: "other", AttemptKey: "cand-1", Branch: "fix-branch",
	}); err == nil {
		t.Fatal("conflicting candidate retry succeeded")
	}
}

func TestClaimSkipsInvestigationJobsUnlessRequested(t *testing.T) {
	database := openTestDB(t)
	if _, _, _, _, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "cat", Name: "bench", BaselineCommit: "base", TargetCommit: "target",
		AttemptKey: "pair-key",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingJob("zig")
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed investigation job %#v without investigation support", claimed)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claimed, err = database.ClaimNextPendingJobIncludingInvestigation("zig", token)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.Kind != JobKindInvestigate {
		t.Fatalf("claimed = %+v", claimed)
	}
}
