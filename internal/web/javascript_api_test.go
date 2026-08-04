package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"opentui-bench/internal/db"
)

func canonicalJSRunBody(t *testing.T) []byte {
	t.Helper()
	cases := []any{
		map[string]any{"category": "JS Layout", "name": "leaf-width-calculate", "workload_version": 1, "parameters": map[string]any{"width": 140, "height": 44, "nodes": 96}},
		map[string]any{"category": "JS Render", "name": "yoga-layout-reads-100", "workload_version": 1, "parameters": map[string]any{"width": 140, "height": 44, "nodes": 100}},
		map[string]any{"category": "JS Mouse", "name": "direct-bubble-depth-8", "workload_version": 1, "parameters": map[string]any{"width": 10, "height": 10, "depth": 8, "input": "direct"}},
		map[string]any{"category": "JS Mouse", "name": "stdin-sgr-bubble-depth-8", "workload_version": 1, "parameters": map[string]any{"width": 10, "height": 10, "depth": 8, "input": "stdin-sgr"}},
	}
	manifest := map[string]any{
		"hash":             canonicalJSManifestHash,
		"protocol_version": 1,
		"measurement": map[string]any{
			"target_batch_ms": 200, "warmup_batches": 5,
			"measured_batches": 20, "max_rsd_ppm": 50000,
			"min_batch_iterations": 1, "max_batch_iterations": 1_000_000_000,
			"max_case_ns": 15_000_000_000, "max_process_ns": 60_000_000_000,
		},
		"cases": cases,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	results := make([]createRunResult, len(cases))
	for caseIndex, identity := range cases {
		identity := identity.(map[string]any)
		variance := 100.0
		result := createRunResult{
			Category: identity["category"].(string), Name: identity["name"].(string), MinNs: 100, AvgNs: 110, MaxNs: 120,
			StdDevNs: 10, P50Ns: 110, P95Ns: 119, P99Ns: 119,
			TotalNs: 6600, Iterations: 60, SampleCount: 3, SampleAvgVarianceNs2: &variance,
		}
		sampleVersion, summaryVersion := int64(db.CurrentSampleDataVersion), int64(db.CurrentSummaryVersion)
		result.SampleDataVersion, result.SummaryVersion = &sampleVersion, &summaryVersion
		for i, avg := range []int64{100, 110, 120} {
			rsd := int64(0)
			sample := db.ResultSample{SampleIndex: int64(i), AvgNs: avg, InnerRSDPPM: &rsd}
			for batch := range 20 {
				sample.Batches = append(sample.Batches, db.ResultSampleBatch{BatchIndex: int64(batch), ElapsedNs: avg, Iterations: 1})
			}
			result.Samples = append(result.Samples, sample)
		}
		results[caseIndex] = result
	}
	body, err := json.Marshal(map[string]any{
		"commit_hash": "abc123", "commit_hash_full": "abc123full", "branch": "main", "machine_id": "runner",
		"benchmark_kind": canonicalJSKind, "benchmark_suite": canonicalJSSuite,
		"protocol_version": canonicalJSProtocol, "bun_version": canonicalJSBunVersion,
		"zig_version": canonicalJSZigVersion, "manifest_hash": canonicalJSManifestHash,
		"manifest_json": string(manifestJSON), "results": results,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newAPIStorageServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{db: database}, database
}

func TestCreateJavaScriptRunValidatesAndReturnsEvidence(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(canonicalJSRunBody(t))))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		ID            int64  `json:"id"`
		BenchmarkKind string `json:"benchmark_kind"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.BenchmarkKind != "js" {
		t.Fatalf("create identity = %+v", created)
	}

	detail := httptest.NewRecorder()
	server.handleRun(detail, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/runs/%d", created.ID), nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"inner_rsd_ppm":0`) ||
		!strings.Contains(detail.Body.String(), `"elapsed_ns":120`) || !strings.Contains(detail.Body.String(), canonicalJSManifestHash) {
		t.Fatalf("detail status/body = %d: %s", detail.Code, detail.Body.String())
	}
}

func TestCreateJavaScriptRunRejectsEvidenceMismatch(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	body := bytes.Replace(canonicalJSRunBody(t), []byte(`"elapsed_ns":120`), []byte(`"elapsed_ns":121`), 1)
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "inconsistent") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateJavaScriptRunRejectsTamperedCanonicalManifest(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	body := bytes.Replace(canonicalJSRunBody(t), []byte(`\"nodes\":96`), []byte(`\"nodes\":97`), 1)
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "recomputed") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRunSelectorsDefaultToZigAndFilterCanonicalJavaScript(t *testing.T) {
	server, database := newAPIStorageServer(t)
	_, err := database.InsertRun(&db.Run{CommitHash: "zig", Branch: "main", RunDate: "2026-08-03T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertRun(&db.Run{CommitHash: "js", Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner",
		BenchmarkKind: "js", BenchmarkSuite: canonicalJSSuite, ProtocolVersion: 1,
		BunVersion: canonicalJSBunVersion, ZigVersion: canonicalJSZigVersion, ManifestHash: canonicalJSManifestHash})
	if err != nil {
		t.Fatal(err)
	}

	zig := httptest.NewRecorder()
	server.handleLatestCommit(zig, httptest.NewRequest(http.MethodGet, "/api/latest-commit?branch=main", nil))
	if !strings.Contains(zig.Body.String(), `"commit_hash":"zig"`) {
		t.Fatalf("default latest = %s", zig.Body.String())
	}
	javascript := httptest.NewRecorder()
	server.handleLatestCommit(javascript, httptest.NewRequest(http.MethodGet, "/api/latest-commit?branch=main&benchmark_kind=js", nil))
	if !strings.Contains(javascript.Body.String(), `"commit_hash":"js"`) || !strings.Contains(javascript.Body.String(), canonicalJSManifestHash) {
		t.Fatalf("JavaScript latest = %s", javascript.Body.String())
	}
}

func TestRunDetailEnforcesExplicitIdentityFilter(t *testing.T) {
	server, database := newAPIStorageServer(t)
	id, err := database.InsertRun(&db.Run{CommitHash: "js", BenchmarkKind: "js", BenchmarkSuite: canonicalJSSuite,
		ProtocolVersion: 1, BunVersion: canonicalJSBunVersion, ZigVersion: canonicalJSZigVersion, ManifestHash: canonicalJSManifestHash})
	if err != nil {
		t.Fatal(err)
	}

	unfiltered := httptest.NewRecorder()
	server.handleRun(unfiltered, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/runs/%d", id), nil))
	if unfiltered.Code != http.StatusOK {
		t.Fatalf("unfiltered status = %d: %s", unfiltered.Code, unfiltered.Body.String())
	}
	mismatch := httptest.NewRecorder()
	server.handleRun(mismatch, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/runs/%d?benchmark_kind=zig", id), nil))
	if mismatch.Code != http.StatusNotFound {
		t.Fatalf("mismatch status = %d: %s", mismatch.Code, mismatch.Body.String())
	}
}

func TestListJobsFiltersBeforeLimit(t *testing.T) {
	server, database := newAPIStorageServer(t)
	_, err := database.InsertJob(&db.Job{Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "none",
		CreatedAt: "2026-08-03T00:00:00Z", RequestedBy: "worker", BenchmarkKind: "js", BenchmarkSuite: canonicalJSSuite,
		ProtocolVersion: canonicalJSProtocol, ManifestHash: canonicalJSManifestHash})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertJob(&db.Job{Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu",
		CreatedAt: "2026-08-04T00:00:00Z", RequestedBy: "other", BenchmarkKind: "zig"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleListJobs(recorder, httptest.NewRequest(http.MethodGet, "/api/jobs?status=failed&benchmark_kind=js&requested_by=worker&limit=1", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"benchmark_kind":"js"`) {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestJavaScriptJobRequiresCanonicalIdentity(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	invalid := httptest.NewRecorder()
	server.handleCreateJob(invalid, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{"branch":"main","benchmark_kind":"js"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d: %s", invalid.Code, invalid.Body.String())
	}

	valid := httptest.NewRecorder()
	body := fmt.Sprintf(`{"branch":"main","benchmark_kind":"js","benchmark_suite":"core-default","protocol_version":1,"manifest_hash":%q,"samples":3,"profile":"none"}`, canonicalJSManifestHash)
	server.handleCreateJob(valid, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(body)))
	if valid.Code != http.StatusCreated || !strings.Contains(valid.Body.String(), canonicalJSManifestHash) {
		t.Fatalf("valid status/body = %d: %s", valid.Code, valid.Body.String())
	}
}

func TestRegressionEndpointRejectsJavaScriptRun(t *testing.T) {
	server, database := newAPIStorageServer(t)
	runID, err := database.InsertRun(&db.Run{CommitHash: "js", Branch: "main", RunDate: "2026-08-04T00:00:00Z",
		BenchmarkKind: "js", BenchmarkSuite: canonicalJSSuite, ProtocolVersion: 1,
		BunVersion: canonicalJSBunVersion, ZigVersion: canonicalJSZigVersion, ManifestHash: canonicalJSManifestHash})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleRegressions(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d", runID), nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "only for Zig") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}
