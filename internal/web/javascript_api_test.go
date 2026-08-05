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
	"opentui-bench/internal/jsbench"
)

func canonicalJSRunBody(t *testing.T) []byte {
	t.Helper()
	cases := []any{
		map[string]any{"category": "JS Layout", "name": "leaf-width-calculate", "workload_version": 2, "parameters": map[string]any{"width": 140, "height": 44, "nodes": 96}},
		map[string]any{"category": "JS Render", "name": "yoga-layout-reads-100", "workload_version": 1, "parameters": map[string]any{"width": 140, "height": 44, "nodes": 100}},
		map[string]any{"category": "JS Mouse", "name": "direct-bubble-depth-8", "workload_version": 1, "parameters": map[string]any{"width": 10, "height": 10, "depth": 8, "input": "direct"}},
		map[string]any{"category": "JS Mouse", "name": "stdin-sgr-bubble-depth-8", "workload_version": 1, "parameters": map[string]any{"width": 10, "height": 10, "depth": 8, "input": "stdin-sgr"}},
		map[string]any{"category": "JS Text Table", "name": "proportional-column-widths", "workload_version": 1, "parameters": map[string]any{"allocations_per_operation": 1, "mix": "alternating", "min_width": 1, "ordinary_widths": "4,49,4,54,38", "ordinary_target_width": 104, "remainder_columns": 64, "remainder_width": 17, "remainder_target_width": 584}},
		map[string]any{"category": "JS Text", "name": "text-buffer-word-wrap-measure", "workload_version": 1, "parameters": map[string]any{"width_method": "unicode", "wrap_mode": "word", "logical_lines": 64, "tokens_per_line": 128, "line_columns": 767, "text_bytes": 49_151, "width_a": 72, "width_b": 78, "measure_height": 2_048}},
		map[string]any{"category": "JS Buffer", "name": "draw-box-titled-scissored", "workload_version": 1, "parameters": map[string]any{"buffer_width": 80, "buffer_height": 24, "width_method": "unicode", "box_x": 2, "box_y": 2, "box_width": 76, "box_height": 20, "scissor_x": 0, "scissor_y": 0, "scissor_width": 72, "scissor_height": 24, "border_style": "rounded", "should_fill": true, "titles_per_box": 2, "title_variants": 2, "visible_cells": 1_400}},
	}
	manifest := map[string]any{
		"hash":             jsbench.ManifestDigest,
		"protocol_version": 1,
		"measurement": map[string]any{
			"target_batch_ms": 200, "warmup_batches": 5,
			"measured_batches": 20, "max_rsd_ppm": 50000,
			"min_batch_iterations": 1, "max_batch_iterations": 1_000_000_000,
			"max_case_ns": 15_000_000_000, "max_process_ns": 75_000_000_000,
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
		"benchmark_kind": jsbench.Kind, "benchmark_suite": jsbench.Suite,
		"protocol_version": jsbench.Protocol, "bun_version": jsbench.BunVersion,
		"zig_version": jsbench.ZigVersion, "manifest_hash": jsbench.ManifestDigest,
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

func insertHistoricalJSCompareRun(t *testing.T, database *db.DB, hash, manifest string, median int64) int64 {
	t.Helper()
	runID, err := database.InsertRun(&db.Run{
		CommitHash: hash, MachineID: "runner", BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: jsbench.Protocol, BunVersion: jsbench.BunVersion,
		ZigVersion: jsbench.ZigVersion, ManifestHash: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertResult(&db.Result{
		RunID: runID, Category: "JS Layout", Name: "leaf", MinNs: median, AvgNs: median,
		MaxNs: median, P50Ns: median, TotalNs: median, Iterations: 1, SampleCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return runID
}

func TestJavaScriptRunCapability(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	recorder := httptest.NewRecorder()
	server.handleCapabilities(recorder, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"javascript_runs":0,"job_lease_protocol":2}` {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}

	server.javascriptRuns = true
	recorder = httptest.NewRecorder()
	server.handleCapabilities(recorder, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"javascript_runs":1,"job_lease_protocol":2}` {
		t.Fatalf("enabled status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestJavaScriptRunCapabilityRequiresExplicitEnvironmentOptIn(t *testing.T) {
	_, database := newAPIStorageServer(t)
	for _, value := range []string{"", "0", "true", "1"} {
		t.Run(fmt.Sprintf("value_%q", value), func(t *testing.T) {
			t.Setenv("BENCH_ENABLE_JAVASCRIPT_RUNS", value)
			t.Setenv("SVG_CACHE_DIR", t.TempDir())
			server, err := NewServer(database, ":0")
			if err != nil {
				t.Fatal(err)
			}
			if server.javascriptRuns != (value == "1") {
				t.Fatalf("javascriptRuns = %v for %q", server.javascriptRuns, value)
			}
		})
	}
}

func TestCreateJavaScriptRunValidatesAndReturnsEvidence(t *testing.T) {
	server, database := newAPIStorageServer(t)
	server.javascriptRuns = true
	var request map[string]any
	if err := json.Unmarshal(canonicalJSRunBody(t), &request); err != nil {
		t.Fatal(err)
	}
	request["manifest_json"] = " \n" + request["manifest_json"].(string) + " \t"
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
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
	stored, err := database.GetRun(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := jsbench.DecodeManifest([]byte(request["manifest_json"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	canonicalManifest, err := jsbench.CanonicalManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ManifestJSON != string(canonicalManifest) || !strings.Contains(stored.ManifestJSON, jsbench.ManifestDigest) {
		t.Fatalf("stored manifest is not canonical and complete: %q", stored.ManifestJSON)
	}

	detail := httptest.NewRecorder()
	server.handleRun(detail, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/runs/%d", created.ID), nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"inner_rsd_ppm":0`) ||
		!strings.Contains(detail.Body.String(), `"elapsed_ns":120`) || !strings.Contains(detail.Body.String(), jsbench.ManifestDigest) {
		t.Fatalf("detail status/body = %d: %s", detail.Code, detail.Body.String())
	}
}

func TestCreateJavaScriptRunEnforcesManifestEvidenceBounds(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"batch iterations", `"iterations":1`, `"iterations":1000000001`, "invalid evidence"},
		{"case elapsed", `"elapsed_ns":100`, `"elapsed_ns":15000000001`, "max_case_ns"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newAPIStorageServer(t)
			server.javascriptRuns = true
			body := bytes.Replace(canonicalJSRunBody(t), []byte(test.old), []byte(test.new), 1)
			recorder := httptest.NewRecorder()
			server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.want) {
				t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCreateJavaScriptRunEnforcesProcessElapsedBudget(t *testing.T) {
	var request struct {
		ManifestJSON string            `json:"manifest_json"`
		Results      []createRunResult `json:"results"`
	}
	if err := json.Unmarshal(canonicalJSRunBody(t), &request); err != nil {
		t.Fatal(err)
	}
	manifest, err := jsbench.DecodeManifest([]byte(request.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	maxCaseNS := int64(3_000)
	maxProcessNS := int64(len(request.Results)*2_400 - 500)
	manifest.Measurement.MaxCaseNS = &maxCaseNS
	manifest.Measurement.MaxProcessNS = &maxProcessNS
	var processTotals [jsbench.Samples]int64
	for i := range request.Results {
		err = validateJSStoredResult(&request.Results[i], manifest.Measurement, &processTotals)
		if i < len(request.Results)-1 && err != nil {
			t.Fatalf("result %d: %v", i, err)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "max_process_ns") {
		t.Fatalf("error = %v, want max_process_ns rejection", err)
	}
}

func TestCreateJavaScriptRunRejectsEvidenceMismatch(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	server.javascriptRuns = true
	body := bytes.Replace(canonicalJSRunBody(t), []byte(`"elapsed_ns":120`), []byte(`"elapsed_ns":121`), 1)
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "inconsistent") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateJavaScriptRunRejectsTamperedCanonicalManifest(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	server.javascriptRuns = true
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
	_, err = database.InsertRun(&db.Run{
		CommitHash: "historical-js", Branch: "main", RunDate: "2026-08-05T00:00:00Z", MachineID: "runner",
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: 1,
		BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion, ManifestHash: "sha256:historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertRun(&db.Run{
		CommitHash: "js", Branch: "main", RunDate: "2026-08-04T00:00:00Z", MachineID: "runner",
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: 1,
		BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
	})
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
	if !strings.Contains(javascript.Body.String(), `"commit_hash":"js"`) || !strings.Contains(javascript.Body.String(), jsbench.ManifestDigest) {
		t.Fatalf("JavaScript latest = %s", javascript.Body.String())
	}
	javascriptRuns := httptest.NewRecorder()
	server.handleRuns(javascriptRuns, httptest.NewRequest(http.MethodGet, "/api/runs?benchmark_kind=js", nil))
	if !strings.Contains(javascriptRuns.Body.String(), `"commit_hash":"js"`) || strings.Contains(javascriptRuns.Body.String(), "historical-js") {
		t.Fatalf("JavaScript runs = %s", javascriptRuns.Body.String())
	}
}

func TestHistoricalJavaScriptCompareByIDUsesStoredIdentity(t *testing.T) {
	server, database := newAPIStorageServer(t)
	baselineID := insertHistoricalJSCompareRun(t, database, "historical-a", "sha256:historical", 100)
	currentID := insertHistoricalJSCompareRun(t, database, "historical-b", "sha256:historical", 110)
	recorder := httptest.NewRecorder()
	server.handleCompare(recorder, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/compare?id_a=%d&id_b=%d&benchmark_kind=js", baselineID, currentID), nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"manifest_hash":"sha256:historical"`) ||
		!strings.Contains(recorder.Body.String(), `"baseline_ns":100,"current_ns":110`) {
		t.Fatalf("ID compare status/body = %d: %s", recorder.Code, recorder.Body.String())
	}

	commitRecorder := httptest.NewRecorder()
	server.handleCompare(commitRecorder, httptest.NewRequest(http.MethodGet,
		"/api/compare?a=historical-a&b=historical-b&benchmark_kind=js", nil))
	if commitRecorder.Code != http.StatusNotFound {
		t.Fatalf("commit compare status/body = %d: %s", commitRecorder.Code, commitRecorder.Body.String())
	}
}

func TestHistoricalJavaScriptCompareByIDRejectsDifferentStoredIdentity(t *testing.T) {
	server, database := newAPIStorageServer(t)
	baselineID := insertHistoricalJSCompareRun(t, database, "historical-a", "sha256:historical-a", 100)
	currentID := insertHistoricalJSCompareRun(t, database, "historical-b", "sha256:historical-b", 110)
	recorder := httptest.NewRecorder()
	server.handleCompare(recorder, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/compare?id_a=%d&id_b=%d&benchmark_kind=js", baselineID, currentID), nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "different benchmark identities") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestHistoricalJavaScriptTrendUsesReferenceCohortWithoutSamples(t *testing.T) {
	server, database := newAPIStorageServer(t)
	insert := func(hash, date string, avg int64) int64 {
		t.Helper()
		runID, err := database.InsertRun(&db.Run{
			CommitHash: hash, Branch: "main", RunDate: date, MachineID: "runner",
			BenchmarkKind: "js", BenchmarkSuite: "historical-suite", ProtocolVersion: 1,
			BunVersion: "1.2.0", ZigVersion: "0.14.0", ManifestHash: "sha256:historical",
		})
		if err != nil {
			t.Fatal(err)
		}
		resultID, err := database.InsertResult(&db.Result{
			RunID: runID, Category: "JS Layout", Name: "leaf", MinNs: avg, AvgNs: avg, MaxNs: avg,
			TotalNs: avg, Iterations: 1, SampleCount: 1,
			Samples: []db.ResultSample{{SampleIndex: 0, AvgNs: avg}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resultID
	}
	oldResult := insert("old", "2026-07-01T00:00:00Z", 100)
	referenceResult := insert("reference", "2026-07-02T00:00:00Z", 110)
	if _, err := database.Exec(`DROP TABLE result_sample_batches`); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.handleTrend(recorder, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/trend?result_id=%d&benchmark_kind=js", referenceResult), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Points []struct {
			ResultID int64 `json:"result_id"`
		} `json:"points"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Points) != 2 || response.Points[0].ResultID != referenceResult || response.Points[1].ResultID != oldResult {
		t.Fatalf("points = %+v, want reference cohort [%d %d]", response.Points, referenceResult, oldResult)
	}
	if strings.Contains(recorder.Body.String(), `"samples"`) || strings.Contains(recorder.Body.String(), `"batches"`) {
		t.Fatalf("trend returned raw evidence: %s", recorder.Body.String())
	}
}

func TestRunDetailEnforcesExplicitIdentityFilter(t *testing.T) {
	server, database := newAPIStorageServer(t)
	id, err := database.InsertRun(&db.Run{
		CommitHash: "js", BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: 1, BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
	})
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
	_, err := database.InsertJob(&db.Job{
		Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "none",
		CreatedAt: "2026-08-03T00:00:00Z", RequestedBy: "worker", BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: jsbench.Protocol, ManifestHash: jsbench.ManifestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.InsertJob(&db.Job{
		Status: "failed", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu",
		CreatedAt: "2026-08-04T00:00:00Z", RequestedBy: "other", BenchmarkKind: "zig",
	})
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
	server.javascriptRuns = true
	invalid := httptest.NewRecorder()
	server.handleCreateJob(invalid, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{"branch":"main","benchmark_kind":"js"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d: %s", invalid.Code, invalid.Body.String())
	}

	valid := httptest.NewRecorder()
	body := fmt.Sprintf(`{"branch":"main","benchmark_kind":"js","benchmark_suite":"core-default","protocol_version":1,"manifest_hash":%q,"samples":3,"profile":"none"}`, jsbench.ManifestDigest)
	server.handleCreateJob(valid, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(body)))
	if valid.Code != http.StatusCreated || !strings.Contains(valid.Body.String(), jsbench.ManifestDigest) {
		t.Fatalf("valid status/body = %d: %s", valid.Code, valid.Body.String())
	}
}

func TestDisabledJavaScriptWritesCannotBypassCapability(t *testing.T) {
	server, database := newAPIStorageServer(t)

	run := httptest.NewRecorder()
	server.handleCreateRun(run, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(canonicalJSRunBody(t))))
	if run.Code != http.StatusForbidden || !strings.Contains(run.Body.String(), "disabled") {
		t.Fatalf("run status/body = %d: %s", run.Code, run.Body.String())
	}

	job := httptest.NewRecorder()
	body := fmt.Sprintf(`{"branch":"main","benchmark_kind":"js","benchmark_suite":"core-default","protocol_version":1,"manifest_hash":%q,"samples":3,"profile":"none"}`, jsbench.ManifestDigest)
	server.handleCreateJob(job, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(body)))
	if job.Code != http.StatusForbidden || !strings.Contains(job.Body.String(), "disabled") {
		t.Fatalf("job status/body = %d: %s", job.Code, job.Body.String())
	}

	var runCount, jobCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || jobCount != 0 {
		t.Fatalf("disabled writes persisted runs=%d jobs=%d", runCount, jobCount)
	}
}

func TestDisabledJavaScriptClaimsOnlySelectZig(t *testing.T) {
	server, database := newAPIStorageServer(t)
	jsID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "none",
		CreatedAt: "2026-08-03T00:00:00Z", BenchmarkKind: jsbench.Kind, BenchmarkSuite: jsbench.Suite,
		ProtocolVersion: jsbench.Protocol, ManifestHash: jsbench.ManifestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	zigID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu",
		CreatedAt: "2026-08-04T00:00:00Z", BenchmarkKind: "zig",
	})
	if err != nil {
		t.Fatal(err)
	}

	unfiltered := httptest.NewRecorder()
	server.handleClaimJob(unfiltered, leaseClaimRequest("/api/jobs/claim"))
	if unfiltered.Code != http.StatusOK || !strings.Contains(unfiltered.Body.String(), fmt.Sprintf(`"id":%d`, zigID)) {
		t.Fatalf("unfiltered claim status/body = %d: %s", unfiltered.Code, unfiltered.Body.String())
	}

	explicitJS := httptest.NewRecorder()
	server.handleClaimJob(explicitJS, leaseClaimRequest("/api/jobs/claim?benchmark_kind=js"))
	if explicitJS.Code != http.StatusNoContent {
		t.Fatalf("explicit JS claim status/body = %d: %s", explicitJS.Code, explicitJS.Body.String())
	}
	storedJS, err := database.GetJob(jsID)
	if err != nil {
		t.Fatal(err)
	}
	if storedJS.Status != "pending" || storedJS.ClaimToken != "" {
		t.Fatalf("disabled JS job was claimed: %+v", storedJS)
	}

	server.javascriptRuns = true
	enabledJS := httptest.NewRecorder()
	server.handleClaimJob(enabledJS, leaseClaimRequest("/api/jobs/claim?benchmark_kind=js"))
	if enabledJS.Code != http.StatusOK || !strings.Contains(enabledJS.Body.String(), fmt.Sprintf(`"id":%d`, jsID)) {
		t.Fatalf("enabled JS claim status/body = %d: %s", enabledJS.Code, enabledJS.Body.String())
	}
}

func TestRegressionEndpointRejectsJavaScriptRun(t *testing.T) {
	server, database := newAPIStorageServer(t)
	runID, err := database.InsertRun(&db.Run{
		CommitHash: "js", Branch: "main", RunDate: "2026-08-04T00:00:00Z",
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: 1,
		BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion, ManifestHash: jsbench.ManifestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleRegressions(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/regressions?run_id=%d", runID), nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "only for Zig") {
		t.Fatalf("status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
}
