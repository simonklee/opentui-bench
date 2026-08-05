package record

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"opentui-bench/internal/jsbench"
)

func jsTestMeta() RunMetadata {
	return RunMetadata{
		BenchmarkSuite: "core-default", ProtocolVersion: 1,
		BunVersion: "1.3.14", ZigVersion: "0.15.2",
		ManifestHash: "sha256:4923157cc57f50a8e1d4cd0229a803654c4cbd115402a1db26ae36f08f2a98aa",
	}
}

func jsTestInvocation(elapsedA, elapsedB, rsd int64) string {
	return fmt.Sprintf(`{"schema_version":1,"benchmark_suite":"core-default","protocol_version":1,"bun_version":"1.3.14","zig_version":"0.15.2","manifest":{"hash":"sha256:4923157cc57f50a8e1d4cd0229a803654c4cbd115402a1db26ae36f08f2a98aa","protocol_version":1,"measurement":{"target_batch_ms":200,"warmup_batches":5,"measured_batches":2,"max_rsd_ppm":20000,"min_batch_iterations":1,"max_batch_iterations":1000000000,"max_case_ns":15000000000,"max_process_ns":60000000000},"cases":[{"category":"JS Layout","name":"leaf","workload_version":1,"parameters":{"width":140}}]},"results":[{"category":"JS Layout","name":"leaf","batch_iterations":1,"batch_elapsed_ns":[%d,%d],"inner_rsd_ppm":%d}]}`, elapsedA, elapsedB, rsd)
}

func jsReaders(documents ...string) []io.Reader {
	readers := make([]io.Reader, len(documents))
	for i := range documents {
		readers[i] = strings.NewReader(documents[i])
	}
	return readers
}

func TestParseJSInvocationsAggregatesAndRetainsEvidence(t *testing.T) {
	formatted := strings.Replace(jsTestInvocation(200, 202, 7036), `{"width":140}`, `{ "width" : 140 }`, 1)
	parsed, err := ParseJSInvocations(jsReaders(
		jsTestInvocation(100, 102, 14002),
		formatted,
		jsTestInvocation(300, 302, 4698),
	), jsTestMeta())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Meta.BenchmarkKind != "js" || parsed.Meta.ManifestJSON == "" || len(parsed.Results) != 1 {
		t.Fatalf("parsed metadata/results = %+v/%d", parsed.Meta, len(parsed.Results))
	}
	document, err := decodeJSInvocation(strings.NewReader(formatted))
	if err != nil {
		t.Fatal(err)
	}
	wantManifest, err := jsbench.CanonicalManifestJSON(document.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Meta.ManifestJSON != string(wantManifest) || strings.Contains(parsed.Meta.ManifestJSON, `{ "width"`) {
		t.Fatalf("stored manifest is not canonical: %s", parsed.Meta.ManifestJSON)
	}
	result := parsed.Results[0]
	if result.MinNs != 100 || result.AvgNs != 201 || result.MaxNs != 302 || result.TotalNs != 1206 || result.Iterations != 6 || result.SampleCount != 3 {
		t.Fatalf("aggregate = %+v", result)
	}
	if len(result.Samples) != 3 {
		t.Fatalf("samples = %+v", result.Samples)
	}
	wantRSD := []int64{14002, 7036, 4698}
	for i, sample := range result.Samples {
		if sample.SampleIndex != int64(i) || sample.InnerRSDPPM == nil || *sample.InnerRSDPPM != wantRSD[i] || len(sample.Batches) != 2 {
			t.Fatalf("sample %d = %+v", i, sample)
		}
		if sample.Batches[1].BatchIndex != 1 || sample.Batches[1].Iterations != 1 {
			t.Fatalf("sample %d batches = %+v", i, sample.Batches)
		}
	}
}

func TestParseJSInvocationsSchema2UsesGenericRuntimeIdentity(t *testing.T) {
	document := strings.Replace(jsTestInvocation(100, 102, 14002),
		`"schema_version":1`, `"schema_version":2,"js_runtime":"node","runtime_version":"26.4.0"`, 1)
	document = strings.Replace(document, `,"bun_version":"1.3.14"`, ``, 1)
	meta := jsTestMeta()
	meta.BunVersion = ""
	meta.JSRuntime, meta.RuntimeVersion = jsbench.RuntimeNode, jsbench.NodeVersion
	parsed, err := ParseJSInvocations(jsReaders(document, document, document), meta)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Meta.JSRuntime != jsbench.RuntimeNode || parsed.Meta.RuntimeVersion != jsbench.NodeVersion || parsed.Meta.BunVersion != "" || parsed.Meta.LegacyJSIdentity {
		t.Fatalf("runtime identity = %+v", parsed.Meta)
	}
}

func jsBudgetFixture(t *testing.T, maxCaseNS, maxProcessNS int64, elapsed [][]int64) (string, RunMetadata) {
	t.Helper()
	maxRSD, minIterations, maxIterations := int64(0), int64(1), int64(1)
	document := jsInvocation{
		SchemaVersion: 1, BenchmarkSuite: "core-default", ProtocolVersion: 1,
		BunVersion: "test-bun", ZigVersion: "test-zig",
		Manifest: jsbench.Manifest{ProtocolVersion: 1, Measurement: jsbench.Measurement{
			TargetBatchMS: 1, WarmupBatches: 1, MeasuredBatches: 2, MaxRSDPPM: &maxRSD,
			MinBatchIterations: &minIterations, MaxBatchIterations: &maxIterations,
			MaxCaseNS: &maxCaseNS, MaxProcessNS: &maxProcessNS,
		}},
	}
	for i, batches := range elapsed {
		name := fmt.Sprintf("case-%d", i)
		document.Manifest.Cases = append(document.Manifest.Cases, jsbench.Case{
			Category: "Test", Name: name, WorkloadVersion: 1, Parameters: json.RawMessage(`{"size":1}`),
		})
		rsd := int64(0)
		document.Results = append(document.Results, jsResult{
			Category: "Test", Name: name, BatchIterations: 1, BatchElapsedNS: batches, InnerRSDPPM: &rsd,
		})
	}
	var err error
	document.Manifest.Hash, err = jsbench.ManifestHash(document.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), RunMetadata{
		BenchmarkSuite: "core-default", ProtocolVersion: 1, BunVersion: "test-bun",
		ZigVersion: "test-zig", ManifestHash: document.Manifest.Hash,
	}
}

func TestParseJSInvocationsEnforcesMeasuredElapsedBudgets(t *testing.T) {
	tests := []struct {
		name       string
		maxCaseNS  int64
		maxProcess int64
		elapsed    [][]int64
		want       string
	}{
		{"case", 15, 100, [][]int64{{8, 8}}, "max_case_ns"},
		{"process", 10, 15, [][]int64{{4, 4}, {4, 4}}, "max_process_ns"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, meta := jsBudgetFixture(t, test.maxCaseNS, test.maxProcess, test.elapsed)
			_, err := ParseJSInvocations(jsReaders(document, document, document), meta)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %s rejection", err, test.want)
			}
		})
	}
}

func TestParseJSInvocationsRejectsProtocolViolations(t *testing.T) {
	valid := jsTestInvocation(100, 102, 14002)
	tests := []struct {
		name string
		docs []string
		meta RunMetadata
	}{
		{"wrong invocation count", []string{valid, valid}, jsTestMeta()},
		{"trailing document", []string{valid + `{}`, valid, valid}, jsTestMeta()},
		{"unknown field", []string{strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"extra":true`, 1), valid, valid}, jsTestMeta()},
		{"wrong version", []string{valid, valid, valid}, func() RunMetadata { m := jsTestMeta(); m.BunVersion = "other"; return m }()},
		{"unstable", []string{jsTestInvocation(100, 200, 471405), valid, valid}, jsTestMeta()},
		{"wrong emitted rsd", []string{jsTestInvocation(100, 102, 1), valid, valid}, jsTestMeta()},
		{"unsafe elapsed", []string{jsTestInvocation(jsbench.MaxSafeInteger+1, jsbench.MaxSafeInteger+1, 0), valid, valid}, jsTestMeta()},
		{"manifest protocol mismatch", []string{strings.Replace(valid, `"protocol_version":1,"measurement"`, `"protocol_version":2,"measurement"`, 1), valid, valid}, jsTestMeta()},
		{"missing measurement policy", []string{strings.Replace(valid, `,"max_process_ns":60000000000`, ``, 1), valid, valid}, jsTestMeta()},
		{"batch iterations outside calibration bounds", []string{strings.Replace(valid, `"batch_iterations":1`, `"batch_iterations":1000000001`, 1), valid, valid}, jsTestMeta()},
		{"tampered manifest hash", []string{strings.Replace(valid, `"width":140`, `"width":141`, 1), valid, valid}, jsTestMeta()},
		{"invalid parameter value", []string{strings.Replace(valid, `{"width":140}`, `{"width":[140]}`, 1), valid, valid}, jsTestMeta()},
		{"result order mismatch", []string{strings.Replace(valid, `"name":"leaf","batch_iterations"`, `"name":"other","batch_iterations"`, 1), valid, valid}, jsTestMeta()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseJSInvocations(jsReaders(test.docs...), test.meta); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestParseJSInvocationsAcceptsHarnessCLIShape(t *testing.T) {
	// Generated by runBenchmarkCli(["--format=json"], ...) in js-benchmark-harness.test.ts.
	const output = `{"schema_version":1,"benchmark_suite":"core-default","protocol_version":1,"bun_version":"test-bun","zig_version":"test-zig","manifest":{"hash":"sha256:7c2460634c4e3e669d38f6af52439e40c89194339cf5040b7be7d656a74e52d6","protocol_version":1,"measurement":{"target_batch_ms":0.000001,"warmup_batches":1,"measured_batches":2,"max_rsd_ppm":1000000,"min_batch_iterations":1,"max_batch_iterations":1,"max_case_ns":15000000000,"max_process_ns":60000000000},"cases":[{"category":"Test","name":"case","workload_version":1,"parameters":{"size":1}}]},"results":[{"category":"Test","name":"case","batch_iterations":1,"batch_elapsed_ns":[1,1],"inner_rsd_ppm":0}]}`
	meta := RunMetadata{
		BenchmarkSuite: "core-default", ProtocolVersion: 1, BunVersion: "test-bun", ZigVersion: "test-zig",
		ManifestHash: "sha256:7c2460634c4e3e669d38f6af52439e40c89194339cf5040b7be7d656a74e52d6",
	}
	parsed, err := ParseJSInvocations(jsReaders(output, output, output), meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Results) != 1 || parsed.Results[0].AvgNs != 1 || parsed.Results[0].SampleCount != 3 {
		t.Fatalf("parsed fixture = %+v", parsed.Results)
	}
}

func TestCalculateRSDPPMUsesSampleStandardDeviation(t *testing.T) {
	got, err := jsbench.CalculateRSDPPM([]float64{1500, 1520})
	if err != nil {
		t.Fatal(err)
	}
	if got != 9366 {
		t.Fatalf("RSD = %d ppm, want 9366", got)
	}
	got, err = jsbench.CalculateRSDPPM([]float64{1, 1})
	if err != nil || got != 0 {
		t.Fatalf("zero RSD = %d, %v", got, err)
	}
}
