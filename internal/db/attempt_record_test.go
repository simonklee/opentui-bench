package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testAttemptRetention() ProfileRetention {
	return ProfileRetention{MaxRuns: 10, MaxBytes: 1 << 20}
}

func createTestInvestigation(t *testing.T, database *DB, key string) (*Investigation, *InvestigationAttempt) {
	t.Helper()
	inv, a, _, _, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "cat", Name: "bench", BaselineCommit: "base", TargetCommit: "target", AttemptKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return inv, a
}

func attemptPayload(t *testing.T, inv *Investigation, a *InvestigationAttempt) ([]AttemptRun, string) {
	t.Helper()
	roles, err := attemptRoles(a)
	if err != nil {
		t.Fatal(err)
	}
	var runs []AttemptRun
	recipes := make(map[string]any)
	for _, role := range roles {
		commit := attemptCommit(inv, a, role)
		side := AttemptRun{
			Run: Run{
				CommitHash: commit, CommitHashFull: commit, Branch: a.Branch,
				RunDate: "2026-09-08T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast", ZigVersion: "0.15.2",
				AttemptKey: a.AttemptKey, AttemptRole: role, Purpose: PurposeInvestigation,
			},
			Results: []Result{investigationResult(inv.Category, inv.Name, 100)},
		}
		side.Results[0].SampleCount = int64(a.Samples)
		for i := range a.Samples {
			side.Results[0].Samples = append(side.Results[0].Samples, ResultSample{SampleIndex: int64(i), AvgNs: 100})
		}
		if a.Profile == "cpu" {
			side.Artifacts = []Artifact{{Kind: "cpu.pprof", DataBlob: []byte(role), Metadata: `{"original":true}`, CreatedAt: side.Run.RunDate}}
		}
		runs = append(runs, side)
		recipes[role] = map[string]any{
			"harness": "native-json-bench", "zig_optimize": "ReleaseFast", "source_dir": "src/zig",
			"zig_version":    "0.15.2",
			"build_commands": [][]string{{"zig", "build", "bench", "-Dbench-optimize=ReleaseFast", "--verbose", "--", "--help"}},
			"filter":         inv.Category, "bench": inv.Name, "samples": a.Samples, "profile": a.Profile,
			"selector_unique": true, "commit_hash": commit,
		}
	}
	encoded, err := json.Marshal(recipes)
	if err != nil {
		t.Fatal(err)
	}
	return runs, string(encoded)
}

func TestPairRetryConflictsAndAdmissionBounds(t *testing.T) {
	database := openTestDB(t)
	inv, _ := createTestInvestigation(t, database, "first")
	create := InvestigationCreate{AttemptKey: "fresh", Samples: MaxInvestigationSamples, Profile: "none"}
	a, job, created, err := database.CreatePairAttempt(inv.ID, create)
	if err != nil || !created {
		t.Fatalf("fresh pair: %+v %v %v", a, created, err)
	}
	if job.CommitHash != inv.TargetCommit || job.BaselineCommit != inv.BaselineCommit || job.ComparisonCommit != inv.TargetCommit {
		t.Fatalf("pair identity: %+v", job)
	}
	retry, retryJob, created, err := database.CreatePairAttempt(inv.ID, create)
	if err != nil || created || retry.ID != a.ID || retryJob.ID != job.ID {
		t.Fatalf("retry: %+v %+v %v %v", retry, retryJob, created, err)
	}
	for name, mutate := range map[string]func(*InvestigationCreate){
		"samples":  func(c *InvestigationCreate) { c.Samples = 2 },
		"profile":  func(c *InvestigationCreate) { c.Profile = "cpu" },
		"branch":   func(c *InvestigationCreate) { c.Branch = "other" },
		"target":   func(c *InvestigationCreate) { c.TargetCommit = "other" },
		"baseline": func(c *InvestigationCreate) { c.BaselineCommit = "other" },
		"selector": func(c *InvestigationCreate) { c.Category = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := create
			mutate(&conflict)
			if _, _, _, err := database.CreatePairAttempt(inv.ID, conflict); err == nil {
				t.Fatal("conflicting retry admitted")
			}
		})
	}
	if _, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: "fresh", TargetCommit: "target", Samples: 30}); err == nil {
		t.Fatal("pair key reused by candidate")
	}
	other, _, _, _, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "other", Name: "bench", BaselineCommit: "base", TargetCommit: "target", AttemptKey: "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.CreatePairAttempt(other.ID, create); err == nil {
		t.Fatal("attempt key reused across investigations")
	}
	for _, samples := range []int{-1, MaxInvestigationSamples + 1} {
		invalid := InvestigationCreate{Category: "invalid", Name: "bench", BaselineCommit: "base", TargetCommit: "target", AttemptKey: "bad", Samples: samples}
		if _, _, _, _, err := database.CreateInvestigationIfAbsent(invalid); err == nil {
			t.Fatalf("initial samples %d admitted", samples)
		}
		if _, _, _, err := database.CreatePairAttempt(inv.ID, invalid); err == nil {
			t.Fatalf("pair samples %d admitted", samples)
		}
		if _, _, _, err := database.CreateCandidateAttempt(inv.ID, invalid); err == nil {
			t.Fatalf("candidate samples %d admitted", samples)
		}
	}
	if _, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: "bad-profile", TargetCommit: "fix", Profile: "heap"}); err == nil {
		t.Fatal("invalid candidate profile admitted")
	}
}

func TestInvestigationBudgetsAreAtomic(t *testing.T) {
	database := openTestDB(t)
	inv, _ := createTestInvestigation(t, database, "initial")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, _ = database.CreatePairAttempt(inv.ID, InvestigationCreate{AttemptKey: fmt.Sprintf("race-%d", i)})
		}()
	}
	wg.Wait()
	attempts, err := database.ListAttempts(inv.ID)
	if err != nil || len(attempts) != MaxOutstandingInvestigationJobs {
		t.Fatalf("outstanding budget: %d attempts, %v", len(attempts), err)
	}
	for i := 0; i < MaxCandidateAttemptsPerInvestigation; i++ {
		if _, err := database.Exec(`UPDATE jobs SET status = 'completed'`); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: fmt.Sprintf("candidate-%d", i), TargetCommit: "fix"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: "candidate-over-budget", TargetCommit: "fix"}); err == nil {
		t.Fatal("candidate budget exceeded")
	}
	for i := MaxOutstandingInvestigationJobs + MaxCandidateAttemptsPerInvestigation; i < MaxAttemptsPerInvestigation; i++ {
		if _, err := database.Exec(`UPDATE jobs SET status = 'completed'`); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := database.CreatePairAttempt(inv.ID, InvestigationCreate{AttemptKey: fmt.Sprintf("pair-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := database.CreatePairAttempt(inv.ID, InvestigationCreate{AttemptKey: "pair-over-budget"}); err == nil {
		t.Fatal("attempt budget exceeded")
	}
	if _, _, created, err := database.CreatePairAttempt(inv.ID, InvestigationCreate{AttemptKey: "initial"}); err != nil || created {
		t.Fatalf("exact retry denied at budget: created=%v err=%v", created, err)
	}
}

func TestRecordAttemptRollsBackWholeSnapshot(t *testing.T) {
	database := openTestDB(t)
	inv, a := createTestInvestigation(t, database, "atomic")
	runs, recipe := attemptPayload(t, inv, a)
	if _, err := database.Exec(`CREATE TRIGGER interrupt_publish BEFORE INSERT ON artifacts
		WHEN NEW.data_blob = CAST('target' AS BLOB)
		BEGIN SELECT RAISE(ABORT, 'interrupted publish'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err == nil {
		t.Fatal("interrupted publish succeeded")
	}
	for _, table := range []string{"runs", "results", "artifacts"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s survived: %d, %v", table, count, err)
		}
	}
	failed, err := database.GetAttemptByKey(a.AttemptKey)
	if err != nil || !reflect.DeepEqual(a, failed) {
		t.Fatalf("attempt changed after rollback: %+v %v", failed, err)
	}
	if _, err := database.Exec(`DROP TRIGGER interrupt_publish`); err != nil {
		t.Fatal(err)
	}
	if _, created, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err != nil || !created {
		t.Fatalf("publish retry: created=%v err=%v", created, err)
	}
}

func TestRecordAttemptAppliesRetentionBeforeCommit(t *testing.T) {
	database := openTestDB(t)
	inv, a := createTestInvestigation(t, database, "bounded")
	runs, recipe := attemptPayload(t, inv, a)
	if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, ProfileRetention{}); err == nil {
		t.Fatal("invalid retention accepted")
	}
	stored, created, err := database.RecordAttempt(a.AttemptKey, recipe, runs, ProfileRetention{MaxRuns: 1, MaxBytes: 6})
	if err != nil || !created {
		t.Fatalf("retention failure left partial evidence: %+v %v", stored, err)
	}
	baseline, err := database.CaptureStatus(stored.BaselineRunID)
	if err != nil || baseline.Complete || baseline.ProfileCount != 0 {
		t.Fatalf("baseline capture was not pruned: %+v %v", baseline, err)
	}
	target, err := database.CaptureStatus(stored.TargetRunID)
	if err != nil || !target.Complete || target.ProfileCount != 1 {
		t.Fatalf("target capture was not retained: %+v %v", target, err)
	}
	_, size, err := profileStorageStats(database)
	if err != nil || size != 6 {
		t.Fatalf("committed profile bytes = %d, %v", size, err)
	}
}

func TestFailedAttemptNeedsFreshKey(t *testing.T) {
	database := openTestDB(t)
	inv, a := createTestInvestigation(t, database, "failed")
	if err := database.FailAttempt(a.AttemptKey, "capture failed"); err != nil {
		t.Fatal(err)
	}
	first, err := database.GetAttemptByKey(a.AttemptKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FailAttempt(a.AttemptKey, "late replacement error"); err != nil {
		t.Fatal(err)
	}
	retry, _, created, err := database.CreatePairAttempt(inv.ID, InvestigationCreate{AttemptKey: a.AttemptKey})
	if err != nil || created || !reflect.DeepEqual(first, retry) {
		t.Fatalf("terminal retry changed failed attempt: %+v %v", retry, err)
	}
	runs, recipe := attemptPayload(t, inv, a)
	if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err == nil {
		t.Fatal("failed attempt was rewound by recording")
	}
}

func TestAttemptSnapshotIsImmutableAcrossLateRetries(t *testing.T) {
	database := openTestDB(t)
	inv, a := createTestInvestigation(t, database, "immutable")
	runs, recipe := attemptPayload(t, inv, a)
	stored, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
	if err != nil {
		t.Fatal(err)
	}
	results, err := database.GetResultsForRun(stored.TargetRunID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := database.GetArtifact(results[0].ID, "cpu.pprof")
	if err != nil {
		t.Fatal(err)
	}
	runs[0].Artifacts[0].DataBlob = []byte("different baseline")
	runs[1].Artifacts[0].Metadata = `{"replacement":true}`
	runs[1].Results[0].AvgNs = 999
	retried, created, err := database.RecordAttempt(a.AttemptKey, strings.ReplaceAll(recipe, "src/zig", "replacement"), runs, testAttemptRetention())
	if err != nil || created || !reflect.DeepEqual(stored, retried) {
		t.Fatalf("snapshot retry changed evidence: %+v %v %v", retried, created, err)
	}
	if err := database.FailAttempt(a.AttemptKey, "late worker failure"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteAttempt(a.AttemptKey, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachRunToAttempt(a.AttemptKey, AttemptRoleTarget, stored.TargetRunID); err != nil {
		t.Fatal(err)
	}
	side, _, created, err := database.InsertRunWithResultsIfAbsent(&runs[1].Run, runs[1].Results)
	if err != nil || created || side.ID != stored.TargetRunID {
		t.Fatalf("individual existing retry: %+v %v %v", side, created, err)
	}
	after, err := database.GetAttemptByKey(a.AttemptKey)
	if err != nil || !reflect.DeepEqual(stored, after) {
		t.Fatalf("late retry changed terminal state: %+v %v", after, err)
	}
	for _, role := range []string{AttemptRoleBaseline, AttemptRoleCandidate, AttemptRoleControl, ""} {
		if err := database.AttachRunToAttempt(a.AttemptKey, role, stored.TargetRunID); err == nil {
			t.Fatalf("target attached as %q", role)
		}
	}
	artifact, err := database.GetArtifact(results[0].ID, "cpu.pprof")
	if err != nil || !reflect.DeepEqual(original, artifact) {
		t.Fatalf("artifact changed: %+v %v", artifact, err)
	}
	if _, err := database.PruneProfileData(ProfileRetention{MaxRuns: 1, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertArtifactIfMissing(original); err == nil {
		t.Fatal("independent artifact upload resurrected pruned evidence")
	}
	if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetArtifact(results[0].ID, "cpu.pprof"); err != sql.ErrNoRows {
		t.Fatalf("atomic retry resurrected pruned evidence: %v", err)
	}
}

func TestRecordAttemptRejectsMismatchedPayloads(t *testing.T) {
	for name, mutate := range map[string]func([]AttemptRun) []AttemptRun{
		"missing role":             func(r []AttemptRun) []AttemptRun { return r[:1] },
		"duplicate role":           func(r []AttemptRun) []AttemptRun { r[1].Run.AttemptRole = AttemptRoleBaseline; return r },
		"invalid role":             func(r []AttemptRun) []AttemptRun { r[1].Run.AttemptRole = AttemptRoleCandidate; return r },
		"wrong key":                func(r []AttemptRun) []AttemptRun { r[1].Run.AttemptKey = "other"; return r },
		"wrong attempt ID":         func(r []AttemptRun) []AttemptRun { r[1].Run.AttemptID = 999; return r },
		"wrong run ID":             func(r []AttemptRun) []AttemptRun { r[1].Run.ID = 999; return r },
		"wrong commit":             func(r []AttemptRun) []AttemptRun { r[1].Run.CommitHashFull = "other"; return r },
		"wrong result":             func(r []AttemptRun) []AttemptRun { r[1].Results[0].Name = "other"; return r },
		"wrong result ID":          func(r []AttemptRun) []AttemptRun { r[1].Results[0].ID = 999; return r },
		"wrong artifact result ID": func(r []AttemptRun) []AttemptRun { r[1].Artifacts[0].ResultID = 999; return r },
		"extra result":             func(r []AttemptRun) []AttemptRun { r[1].Results = append(r[1].Results, r[1].Results[0]); return r },
		"wrong artifact":           func(r []AttemptRun) []AttemptRun { r[1].Artifacts[0].Kind = "raw.perf"; return r },
		"extra artifact": func(r []AttemptRun) []AttemptRun {
			r[1].Artifacts = append(r[1].Artifacts, r[1].Artifacts[0])
			return r
		},
		"artifact bytes": func(r []AttemptRun) []AttemptRun {
			r[1].Artifacts[0].DataBlob = bytes.Repeat([]byte{1}, maxAttemptArtifactBytes)
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			database := openTestDB(t)
			inv, a := createTestInvestigation(t, database, "invalid")
			runs, recipe := attemptPayload(t, inv, a)
			if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, mutate(runs), testAttemptRetention()); err == nil {
				t.Fatal("invalid payload recorded")
			}
		})
	}
}

func TestCandidateRecordsAllThreeRolesAndRejectsLegacyPartials(t *testing.T) {
	database := openTestDB(t)
	inv, pair := createTestInvestigation(t, database, "pair")
	a, _, _, err := database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: "candidate", TargetCommit: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	runs, recipe := attemptPayload(t, inv, a)
	if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs[2:], testAttemptRetention()); err == nil {
		t.Fatal("candidate-only upload succeeded")
	}
	if _, _, err := database.RecordAttempt(a.AttemptKey, "{}", runs, testAttemptRetention()); err == nil {
		t.Fatal("empty recipe succeeded")
	}
	if _, _, _, err := database.InsertRunWithResultsIfAbsent(&runs[2].Run, runs[2].Results); err == nil {
		t.Fatal("independent new side upload succeeded")
	}
	stored, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
	if err != nil || stored.BaselineRunID == 0 || stored.TargetRunID == 0 || stored.RunID == stored.TargetRunID {
		t.Fatalf("candidate snapshot: %+v %v", stored, err)
	}
	if err := database.AttachRunToAttempt(pair.AttemptKey, AttemptRoleTarget, stored.TargetRunID); err == nil {
		t.Fatal("cross-attempt run attached")
	}
	legacy, legacyRecipe := attemptPayload(t, inv, pair)
	normalizeRunIdentity(&legacy[0].Run)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = insertRunWithResultsTx(tx, &legacy[0].Run, legacy[0].Results, attemptIdempotencyKey(pair.AttemptKey, AttemptRoleBaseline))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.RecordAttempt(pair.AttemptKey, legacyRecipe, legacy, testAttemptRetention()); err == nil || !strings.Contains(err.Error(), "create a new attempt") {
		t.Fatalf("legacy partial should require fresh attempt: %v", err)
	}
}

func TestAbbreviatedInvestigationCommitsRecordAndComplete(t *testing.T) {
	database := openTestDB(t)
	inv, pair, _, _, err := database.CreateInvestigationIfAbsent(InvestigationCreate{
		Category: "cat", Name: "bench", BaselineCommit: "AbCd", TargetCommit: "DEF012", AttemptKey: "abbreviated-pair",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedInv := *inv
	resolvedInv.BaselineCommit = "abcd" + strings.Repeat("1", 36)
	resolvedInv.TargetCommit = "def012" + strings.Repeat("2", 34)
	candidateCommit := "cafe567" + strings.Repeat("3", 33)
	for _, role := range []string{AttemptRolePair, AttemptRoleCandidate} {
		t.Run(role, func(t *testing.T) {
			a := pair
			resolved := resolvedInv.TargetCommit
			if role == AttemptRoleCandidate {
				a, _, _, err = database.CreateCandidateAttempt(inv.ID, InvestigationCreate{AttemptKey: "abbreviated-candidate", TargetCommit: "CaFe567"})
				if err != nil {
					t.Fatal(err)
				}
				resolved = candidateCommit
			}
			token := strings.Repeat("a", 64)
			job, err := database.ClaimNextPendingJobIncludingInvestigation("zig", token)
			if err != nil || job == nil || job.ID != a.JobID {
				t.Fatalf("claim: %+v %v", job, err)
			}
			if err := database.UpdateJobCommitHash(context.Background(), job.ID, token, resolved); err != nil {
				t.Fatal(err)
			}
			resolvedAttempt := *a
			resolvedAttempt.CommitHash = resolved
			runs, recipe := attemptPayload(t, &resolvedInv, &resolvedAttempt)
			mismatchedRecipe := strings.ReplaceAll(recipe, resolvedInv.BaselineCommit, "abcd"+strings.Repeat("9", 36))
			if _, _, err := database.RecordAttempt(a.AttemptKey, mismatchedRecipe, runs, testAttemptRetention()); err == nil {
				t.Fatal("recipe accepted a different full commit with the same requested prefix")
			}
			stored, created, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
			if err != nil || !created {
				t.Fatalf("record resolved commits: %+v %v", stored, err)
			}
			if err := database.CompleteJob(context.Background(), job.ID, token, stored.RunID); err != nil {
				t.Fatal(err)
			}
			completed, err := database.GetJob(job.ID)
			if err != nil || completed.Status != "completed" || completed.CommitHash != resolved || completed.RunID == nil || *completed.RunID != stored.RunID {
				t.Fatalf("complete resolved job: %+v %v", completed, err)
			}
			retry, created, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
			if err != nil || created || !reflect.DeepEqual(stored, retry) {
				t.Fatalf("exact retry changed snapshot: %+v %v", retry, err)
			}
			for _, side := range runs {
				retriedRun, _, created, err := database.InsertRunWithResultsIfAbsent(&side.Run, side.Results)
				if err != nil || created || retriedRun.CommitHashFull != side.Run.CommitHashFull {
					t.Fatalf("exact side retry: %+v %v", retriedRun, err)
				}
			}
			changedRuns := append([]AttemptRun(nil), runs...)
			changedRuns[0].Run.CommitHashFull = "abcd" + strings.Repeat("9", 36)
			if _, _, err := database.RecordAttempt(a.AttemptKey, mismatchedRecipe, changedRuns, testAttemptRetention()); err == nil {
				t.Fatal("retry accepted a different resolved baseline sharing the requested prefix")
			}
			reread, err := database.GetAttemptByKey(a.AttemptKey)
			if err != nil || !reflect.DeepEqual(stored, reread) || reread.CommitHash != a.CommitHash {
				t.Fatalf("retry changed stored evidence or request pin: %+v %v", reread, err)
			}
		})
	}
	reread, err := database.GetInvestigation(inv.ID)
	if err != nil || !reflect.DeepEqual(inv, reread) {
		t.Fatalf("recording changed investigation identity or request pins: %+v %v", reread, err)
	}
}

func TestRequestedCommitMatching(t *testing.T) {
	full := "abcdef" + strings.Repeat("0", 34)
	for _, tc := range []struct {
		requested string
		resolved  string
		matches   bool
	}{
		{"base", "base", true},
		{"aBcD", full, true},
		{"abcdef", full, true},
		{strings.ToUpper(full), full, true},
		{"abc", full, false},
		{full + "0", full, false},
		{"bcde", full, false},
		{"abcg", full, false},
		{"abcd", full[:39], false},
		{"abcd", full[:39] + "g", false},
		{"main", "main" + strings.Repeat("0", 36), false},
	} {
		if got := requestedCommitMatches(tc.requested, tc.resolved); got != tc.matches {
			t.Errorf("requested %q resolved %q: matched=%v, want %v", tc.requested, tc.resolved, got, tc.matches)
		}
	}
}

func TestRecordAttemptRejectsIncoherentBuildProvenance(t *testing.T) {
	for name, mutate := range map[string]func(*AttemptRun, map[string]any){
		"unoptimized recipe":        func(_ *AttemptRun, r map[string]any) { r["zig_optimize"] = "Debug" },
		"optimizer mismatch":        func(_ *AttemptRun, r map[string]any) { r["zig_optimize"] = "ReleaseSafe" },
		"missing recipe optimizer":  func(_ *AttemptRun, r map[string]any) { delete(r, "zig_optimize") },
		"version mismatch":          func(_ *AttemptRun, r map[string]any) { r["zig_version"] = "0.14.1" },
		"missing recipe version":    func(_ *AttemptRun, r map[string]any) { delete(r, "zig_version") },
		"missing run optimizer":     func(s *AttemptRun, _ map[string]any) { s.Run.ZigOptimize = "" },
		"missing run version":       func(s *AttemptRun, _ map[string]any) { s.Run.ZigVersion = "" },
		"sample count mismatch":     func(s *AttemptRun, _ map[string]any) { s.Results[0].SampleCount = 1 },
		"raw sample count mismatch": func(s *AttemptRun, _ map[string]any) { s.Results[0].Samples = s.Results[0].Samples[:1] },
		"malformed executable hash": func(_ *AttemptRun, r map[string]any) { r["executable_sha256"] = "not-a-digest" },
		"empty build command":       func(_ *AttemptRun, r map[string]any) { r["build_commands"] = [][]string{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			database := openTestDB(t)
			inv, a := createTestInvestigation(t, database, "provenance")
			runs, recipe := attemptPayload(t, inv, a)
			var recipes map[string]map[string]any
			if err := json.Unmarshal([]byte(recipe), &recipes); err != nil {
				t.Fatal(err)
			}
			mutate(&runs[0], recipes[AttemptRoleBaseline])
			modified, err := json.Marshal(recipes)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := database.RecordAttempt(a.AttemptKey, string(modified), runs, testAttemptRetention()); err == nil {
				t.Fatal("incomplete or contradictory provenance recorded")
			}
		})
	}
}

func TestIncoherentArchiveCannotBeRetriedOrCompleteJob(t *testing.T) {
	for _, field := range []string{"zig_optimize", "zig_version", "samples"} {
		t.Run(field, func(t *testing.T) {
			database := openTestDB(t)
			inv, a := createTestInvestigation(t, database, "archive")
			job := claimAttemptJob(t, database, a)
			runs, recipe := attemptPayload(t, inv, a)
			stored, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention())
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "zig_optimize":
				_, err = database.Exec(`UPDATE investigation_attempts SET recipe_json = ? WHERE id = ?`,
					strings.ReplaceAll(recipe, "ReleaseFast", "ReleaseSafe"), a.ID)
			case "zig_version":
				_, err = database.Exec(`UPDATE runs SET zig_version = '' WHERE id = ?`, stored.BaselineRunID)
			case "samples":
				_, err = database.Exec(`UPDATE results SET sample_count = 1 WHERE run_id = ?`, stored.BaselineRunID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := database.RecordAttempt(a.AttemptKey, recipe, runs, testAttemptRetention()); err == nil {
				t.Fatal("incoherent archive was reused as a complete snapshot")
			}
			if err := database.CompleteJob(context.Background(), job.ID, job.ClaimToken, stored.RunID); !errors.Is(err, ErrJobRunMismatch) {
				t.Fatalf("incoherent archive completed job: %v", err)
			}
			stillClaimed, err := database.ClaimNextPendingJobIncludingInvestigation("zig", job.ClaimToken)
			if err != nil || stillClaimed == nil || stillClaimed.Status != "running" || stillClaimed.ID != job.ID || stillClaimed.RunID != nil {
				t.Fatalf("failed completion changed job or claim: %+v %v", stillClaimed, err)
			}
		})
	}
}
