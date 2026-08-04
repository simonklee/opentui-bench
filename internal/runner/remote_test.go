package runner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

func TestRecordRunSendsPrecisionFieldsToLegacyCompatibleServer(t *testing.T) {
	var request createRunRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":7,"result_ids":{"cat/bench":11}}`)
	}))
	defer server.Close()
	variance := 0.5
	rsd := int64(1000)
	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, ids, err := recorder.RecordRun(&record.ParsedRun{Meta: record.RunMetadata{
		BenchmarkKind: "js", BenchmarkSuite: JavaScriptSuite, ProtocolVersion: JavaScriptProtocol,
		BunVersion: JavaScriptBunVersion, ZigVersion: JavaScriptZigVersion,
		ManifestHash: JavaScriptManifestHash, ManifestJSON: `{"hash":"test"}`,
	}, Results: []record.ParsedResult{{
		Category: "cat", Name: "bench", AvgNs: 1, SampleCount: 2,
		SampleAvgVarianceNs2: &variance, SampleDataVersion: 1, SummaryVersion: 2,
		Samples: []db.ResultSample{{SampleIndex: 0, AvgNs: 1, InnerRSDPPM: &rsd,
			Batches: []db.ResultSampleBatch{{BatchIndex: 0, ElapsedNs: 10, Iterations: 10}}}, {SampleIndex: 2, AvgNs: 2}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if ids[db.BenchmarkKey{Category: "cat", Name: "bench"}] != 11 {
		t.Fatalf("legacy result IDs = %+v", ids)
	}
	got := request.Results[0]
	if request.BenchmarkKind != "js" || request.BunVersion != JavaScriptBunVersion || request.ManifestHash != JavaScriptManifestHash {
		t.Fatalf("run identity = %+v", request)
	}
	if got.SampleAvgVarianceNs2 == nil || *got.SampleAvgVarianceNs2 != 0.5 || got.SampleDataVersion != 1 || got.SummaryVersion != 2 ||
		len(got.Samples) != 2 || got.Samples[0].InnerRSDPPM == nil || len(got.Samples[0].Batches) != 1 {
		t.Fatalf("precision payload = %+v", got)
	}
}

func TestRecordRunRejectsAmbiguousLegacyResultKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":7,"result_ids":{"a/b/c":11}}`)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, _, err := recorder.RecordRun(&record.ParsedRun{Results: []record.ParsedResult{
		{Category: "a", Name: "b/c"},
		{Category: "a/b", Name: "c"},
	}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("RecordRun error = %v, want ambiguous legacy key error", err)
	}
}

func TestFinalizeArtifactsUsesAuthenticatedRunEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/7/artifacts/finalize" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL, APIKey: "secret"}
	if err := recorder.FinalizeArtifacts(7); err != nil {
		t.Fatal(err)
	}
}
