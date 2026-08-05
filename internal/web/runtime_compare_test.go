package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
)

func TestRuntimeCompareIsDescriptiveAndRequiresCompatibleCohorts(t *testing.T) {
	server, database := newAPIStorageServer(t)
	insert := func(runtime, version string, p50 int64) int64 {
		run := &db.Run{CommitHash: "abc", CommitHashFull: "abcdef", RunDate: "2026-08-04T00:00:00Z",
			MachineID: "runner", BenchmarkKind: jsbench.Kind, BenchmarkSuite: jsbench.Suite,
			ProtocolVersion: jsbench.Protocol, JSRuntime: runtime, RuntimeVersion: version,
			ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest}
		if runtime == jsbench.RuntimeBun {
			run.BunVersion = version
		}
		runID, _, err := database.InsertRunWithResults(run, []db.Result{{Category: "JS Layout", Name: "leaf",
			MinNs: p50, AvgNs: p50, MaxNs: p50, P50Ns: p50, TotalNs: p50, Iterations: 1, SampleCount: 1}})
		if err != nil {
			t.Fatal(err)
		}
		return runID
	}
	baselineID := insert(jsbench.RuntimeBun, jsbench.BunVersion, 100)
	comparedID := insert(jsbench.RuntimeNode, jsbench.NodeVersion, 80)
	recorder := httptest.NewRecorder()
	server.handleRuntimeCompare(recorder, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/runtime-compare?baseline_run_id=%d&compared_run_id=%d", baselineID, comparedID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Metric      string `json:"metric"`
		LowerBetter bool   `json:"lower_is_better"`
		Comparisons []struct {
			DurationChangePercent float64 `json:"duration_change_percent"`
			SpeedRatio            float64 `json:"speed_ratio"`
		} `json:"comparisons"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Metric != "p50_ns" || !response.LowerBetter || len(response.Comparisons) != 1 ||
		response.Comparisons[0].DurationChangePercent != -20 || response.Comparisons[0].SpeedRatio != 1.25 {
		t.Fatalf("response = %+v", response)
	}
}

func TestRuntimeTrendRetainsUnpairedCommitsAsGaps(t *testing.T) {
	server, database := newAPIStorageServer(t)
	insert := func(commit, date, runtime, version string, p50 int64) int64 {
		run := &db.Run{
			CommitHash: commit[:6], CommitHashFull: commit, RunDate: date,
			MachineID: "runner", BenchmarkKind: jsbench.Kind, BenchmarkSuite: jsbench.Suite,
			ProtocolVersion: jsbench.Protocol, JSRuntime: runtime, RuntimeVersion: version,
			ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
		}
		if runtime == jsbench.RuntimeBun {
			run.BunVersion = version
		}
		_, ids, err := database.InsertRunWithResults(run, []db.Result{{
			Category: "JS Layout", Name: "leaf", MinNs: p50, AvgNs: p50, MaxNs: p50,
			P50Ns: p50, TotalNs: p50, Iterations: 1, SampleCount: 1,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return ids[db.BenchmarkKey{Category: "JS Layout", Name: "leaf"}]
	}
	pairedBunID := insert("abcdef1", "2026-08-04T00:00:00Z", jsbench.RuntimeBun, jsbench.BunVersion, 100)
	insert("abcdef1", "2026-08-04T00:01:00Z", jsbench.RuntimeNode, jsbench.NodeVersion, 80)
	insert("abcdef2", "2026-08-05T00:00:00Z", jsbench.RuntimeBun, jsbench.BunVersion, 120)

	recorder := httptest.NewRecorder()
	server.handleRuntimeTrend(recorder, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/runtime-trend?result_id=%d&limit=10", pairedBunID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Pairs []struct {
			CommitHash       string `json:"commit_hash"`
			BaselineP50Ns    *int64 `json:"baseline_p50_ns"`
			ComparedP50Ns    *int64 `json:"compared_p50_ns"`
			ComparedResultID *int64 `json:"compared_result_id"`
		} `json:"pairs"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Pairs) != 2 || response.Pairs[0].CommitHash != "abcdef2" ||
		response.Pairs[0].BaselineP50Ns == nil || *response.Pairs[0].BaselineP50Ns != 120 ||
		response.Pairs[0].ComparedP50Ns != nil || response.Pairs[0].ComparedResultID != nil ||
		response.Pairs[1].ComparedP50Ns == nil || *response.Pairs[1].ComparedP50Ns != 80 {
		t.Fatalf("pairs = %+v", response.Pairs)
	}
}
