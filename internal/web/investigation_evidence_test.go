package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
	"opentui-bench/internal/runner"
)

func evidenceRecording(t *testing.T, key string, commits []string, samples [][]int64) record.InvestigationRecording {
	t.Helper()
	recording := record.InvestigationRecording{AttemptKey: key}
	recipes := map[string]runner.Recipe{}
	roles := []string{db.AttemptRoleBaseline, db.AttemptRoleTarget, db.AttemptRoleCandidate}
	for i, commit := range commits {
		var outputs []io.Reader
		for _, value := range samples[i] {
			outputs = append(outputs, strings.NewReader(fmt.Sprintf(`{"benchmark":"render","results":[{"name":"work","min_ns":%d,"avg_ns":%d,"max_ns":%d,"total_ns":%d,"iterations":1}]}`, value, value, value, value)))
		}
		parsed, err := record.ParseInvocations(outputs, record.RunMetadata{
			CommitHash: commit[:8], CommitHashFull: commit, Branch: "main",
			MachineID: "test-runner", ZigOptimize: "ReleaseFast", ZigVersion: "test-zig",
			BenchmarkKind: "zig", BenchmarkSuite: "core-default", ProtocolVersion: 1,
			SampleCount: len(outputs), Purpose: db.PurposeInvestigation, AttemptKey: key, AttemptRole: roles[i],
		})
		if err != nil {
			t.Fatal(err)
		}
		recording.Runs = append(recording.Runs, record.InvestigationRun{Run: *parsed})
		recipes[roles[i]] = runner.Recipe{
			CommitHash: commit, Harness: "native-json-bench", ZigVersion: "test-zig", ZigOptimize: "ReleaseFast",
			Filter: "render", Bench: "work", Samples: len(outputs), Profile: "none", SelectorUnique: true,
			BuildCommands: [][]string{{"zig", "build", "-Doptimize=ReleaseFast", "bench"}},
			TimingCommand: []string{"./bench", "--json", "--mem", "--filter", "render", "--bench", "work"},
		}
	}
	var err error
	recording.Recipe, err = json.Marshal(recipes)
	if err != nil {
		t.Fatal(err)
	}
	return recording
}

func TestInvestigationRecordingHTTPIsAtomicAndRetrySafe(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	commits := []string{strings.Repeat("a", 40), strings.Repeat("b", 40)}
	inv, _, _, _, err := database.CreateInvestigationIfAbsent(db.InvestigationCreate{
		Category: "render", Name: "work", BaselineCommit: commits[0], TargetCommit: commits[1], AttemptKey: "pair", Profile: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{db: database, apiKey: "test-key"}
	post := func(recording record.InvestigationRecording) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(recording)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/investigations/%d/record", inv.ID), strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer test-key")
		response := httptest.NewRecorder()
		server.routeInvestigationsAPI(response, request)
		return response
	}
	recording := evidenceRecording(t, "pair", commits, [][]int64{{100, 100, 100}, {200, 200, 200}})
	partial := recording
	partial.Runs = partial.Runs[:1]
	if response := post(partial); response.Code != http.StatusBadRequest {
		t.Fatalf("partial publication: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial publication stored %d runs: %v", count, err)
	}
	first := post(recording)
	if first.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", first.Code, first.Body.String())
	}
	retry := post(evidenceRecording(t, "pair", commits, [][]int64{{1000, 1000, 1000}, {2000, 2000, 2000}}))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
	attempt, err := database.GetAttemptByKey("pair")
	if err != nil {
		t.Fatal(err)
	}
	base, err := server.resultForBenchmark(attempt.BaselineRunID, "render", "work")
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.resultForBenchmark(attempt.TargetRunID, "render", "work")
	if err != nil {
		t.Fatal(err)
	}
	if base.AvgNs != 100 || target.AvgNs != 200 || attempt.Status != "completed" {
		t.Fatalf("retry replaced original snapshot: base=%d target=%d status=%s", base.AvgNs, target.AvgNs, attempt.Status)
	}
	recording.Runs[1].Run.Meta.AttemptRole = "control"
	if response := post(recording); response.Code != http.StatusBadRequest {
		t.Fatalf("role mismatch: %d %s", response.Code, response.Body.String())
	}
}

func TestCandidateEvidenceUsesQualifiedFreshControls(t *testing.T) {
	for _, tc := range []struct {
		name       string
		values     [][]int64
		unchanged  bool
		untimed    int
		wantStatus string
		improved   bool
		badRecipe  bool
	}{
		{"fresh controls", [][]int64{{1000, 1000, 1000}, {2000, 2000, 2000}, {1900, 1900, 1900}}, false, -1, "improved", true, false},
		{"unchanged control", [][]int64{{10, 10, 10}, {20, 20, 20}, {19, 19, 19}}, true, -1, "unchanged_control", false, false},
		{"measurement noise", [][]int64{{80, 80, 80}, {100, 200, 100}, {99, 201, 99}}, false, -1, "inconclusive", false, false},
		{"untimed baseline", [][]int64{{100, 100, 100}, {200, 200, 200}, {150, 150, 150}}, false, 0, "insufficient", false, false},
		{"untimed target", [][]int64{{100, 100, 100}, {200, 200, 200}, {150, 150, 150}}, false, 1, "insufficient", false, false},
		{"untimed candidate", [][]int64{{100, 100, 100}, {200, 200, 200}, {150, 150, 150}}, false, 2, "insufficient", false, false},
		{"contradictory recipe", [][]int64{{100, 100, 100}, {200, 200, 200}, {150, 150, 150}}, false, -1, "insufficient", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			commits := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}
			if tc.unchanged {
				commits[2] = commits[1]
			}
			inv, _, _, _, err := database.CreateInvestigationIfAbsent(db.InvestigationCreate{
				Category: "render", Name: "work", BaselineCommit: commits[0], TargetCommit: commits[1], AttemptKey: "pair", Profile: "none",
			})
			if err != nil {
				t.Fatal(err)
			}
			retention := db.ProfileRetention{MaxRuns: 10, MaxBytes: 1024}
			if _, _, err := record.StoreInvestigation(database, evidenceRecording(t, "pair", commits[:2], [][]int64{{100, 100, 100}, {200, 200, 200}}), retention); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := database.CreateCandidateAttempt(inv.ID, db.InvestigationCreate{
				AttemptKey: "candidate", TargetCommit: commits[2], Profile: "none",
			}); err != nil {
				t.Fatal(err)
			}
			candidate, _, err := record.StoreInvestigation(database, evidenceRecording(t, "candidate", commits, tc.values), retention)
			if err != nil {
				t.Fatal(err)
			}
			if tc.untimed >= 0 {
				runID := []int64{candidate.BaselineRunID, candidate.TargetRunID, candidate.RunID}[tc.untimed]
				if _, err := database.Exec(`UPDATE results SET min_ns=0, avg_ns=0, max_ns=0, std_dev_ns=0, p50_ns=0, p95_ns=0, p99_ns=0, total_ns=0 WHERE run_id=?`, runID); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`DELETE FROM result_samples WHERE result_id IN (SELECT id FROM results WHERE run_id=?)`, runID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.badRecipe {
				var recipes map[string]runner.Recipe
				if err := json.Unmarshal([]byte(candidate.RecipeJSON), &recipes); err != nil {
					t.Fatal(err)
				}
				target := recipes[db.AttemptRoleTarget]
				target.ZigOptimize = "Debug"
				recipes[db.AttemptRoleTarget] = target
				data, err := json.Marshal(recipes)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE investigation_attempts SET recipe_json=? WHERE id=?`, string(data), candidate.ID); err != nil {
					t.Fatal(err)
				}
			}
			attempts, err := database.ListAttempts(inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			server := &Server{db: database}
			bundle, err := server.buildEvidenceBundle(inv, attempts)
			if err != nil {
				t.Fatal(err)
			}
			timing := bundle["timing"].(map[string]any)
			if timing["candidate_comparison_status"] != tc.wantStatus || timing["candidate_improved"] != tc.improved {
				t.Fatalf("candidate outcome: %v", timing)
			}
			if timing["comparison_attempt_id"] != candidate.ID {
				t.Fatalf("used stale pair instead of candidate controls: %v", timing)
			}
			if tc.untimed >= 0 && (timing["reproduced"] == true || inv.Status == "improved") {
				t.Fatalf("untimed evidence produced success: %v status=%s", timing, inv.Status)
			}
		})
	}
}
