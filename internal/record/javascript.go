package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
)

const jsMaxDocumentBytes = 10 << 20

type jsInvocation struct {
	SchemaVersion   int64            `json:"schema_version"`
	BenchmarkSuite  string           `json:"benchmark_suite"`
	ProtocolVersion int64            `json:"protocol_version"`
	BunVersion      string           `json:"bun_version"`
	JSRuntime       string           `json:"js_runtime"`
	RuntimeVersion  string           `json:"runtime_version"`
	ZigVersion      string           `json:"zig_version"`
	Manifest        jsbench.Manifest `json:"manifest"`
	Results         []jsResult       `json:"results"`
}

type jsResult struct {
	Category        string  `json:"category"`
	Name            string  `json:"name"`
	BatchIterations int64   `json:"batch_iterations"`
	BatchElapsedNS  []int64 `json:"batch_elapsed_ns"`
	InnerRSDPPM     *int64  `json:"inner_rsd_ppm"`
}

// ParseJSInvocations validates and aggregates the fixed JavaScript protocol.
// Expected identity is supplied in meta; no output is accepted without all
// pinned fields because those fields define the comparison cohort.
func ParseJSInvocations(invocations []io.Reader, meta RunMetadata) (*ParsedRun, error) {
	if len(invocations) != jsbench.Samples {
		return nil, fmt.Errorf("JavaScript protocol requires exactly 3 invocations, got %d", len(invocations))
	}
	if meta.JSRuntime == "" && meta.BunVersion != "" {
		meta.JSRuntime, meta.RuntimeVersion = jsbench.RuntimeBun, meta.BunVersion
	}
	if meta.BenchmarkSuite == "" || meta.ProtocolVersion <= 0 || meta.JSRuntime == "" || meta.RuntimeVersion == "" ||
		meta.ZigVersion == "" || meta.ManifestHash == "" {
		return nil, fmt.Errorf("complete expected JavaScript identity is required")
	}
	meta.BenchmarkKind = jsbench.Kind
	meta.SampleCount = jsbench.Samples

	var referenceManifest []byte
	var referenceCases []jsbench.Case
	samples := map[db.BenchmarkKey][]sample{}
	var order []db.BenchmarkKey
	for invocationIndex, reader := range invocations {
		document, err := decodeJSInvocation(reader)
		if err != nil {
			return nil, fmt.Errorf("parse JavaScript invocation %d: %w", invocationIndex, err)
		}
		if err := validateJSIdentity(document, meta); err != nil {
			return nil, fmt.Errorf("JavaScript invocation %d: %w", invocationIndex, err)
		}
		if document.SchemaVersion == 1 {
			meta.LegacyJSIdentity = true
		}
		manifestJSON, err := jsbench.CanonicalManifestJSON(document.Manifest)
		if err != nil {
			return nil, fmt.Errorf("marshal manifest: %w", err)
		}
		if invocationIndex == 0 {
			referenceManifest = manifestJSON
			referenceCases = document.Manifest.Cases
			meta.ManifestJSON = string(manifestJSON)
			order = make([]db.BenchmarkKey, len(referenceCases))
			for i, manifestCase := range referenceCases {
				order[i] = db.BenchmarkKey{Category: manifestCase.Category, Name: manifestCase.Name}
			}
		} else if !bytes.Equal(referenceManifest, manifestJSON) {
			return nil, fmt.Errorf("JavaScript invocation %d manifest differs from invocation 0", invocationIndex)
		}

		invocationSamples, err := validateJSResults(document, referenceCases, int64(invocationIndex))
		if err != nil {
			return nil, fmt.Errorf("JavaScript invocation %d: %w", invocationIndex, err)
		}
		for i, key := range order {
			samples[key] = append(samples[key], invocationSamples[i])
		}
	}

	parsed := &ParsedRun{Meta: meta}
	for _, key := range order {
		sampleList := samples[key]
		var totalNS, totalIterations int64
		for _, process := range sampleList {
			var ok bool
			totalNS, ok = addSafe(totalNS, process.totalNs)
			if !ok {
				return nil, fmt.Errorf("aggregate %s/%s total_ns overflow", key.Category, key.Name)
			}
			totalIterations, ok = addSafe(totalIterations, process.iterations)
			if !ok {
				return nil, fmt.Errorf("aggregate %s/%s iterations overflow", key.Category, key.Name)
			}
		}
		aggregate := aggregateSamples(key.Category, key.Name, sampleList)
		aggregate.TotalNs = totalNS
		aggregate.Iterations = totalIterations
		parsed.Results = append(parsed.Results, ParsedResult{
			Category: key.Category, Name: key.Name, MinNs: aggregate.MinNs, AvgNs: aggregate.AvgNs,
			MaxNs: aggregate.MaxNs, StdDevNs: aggregate.StdDevNs, P50Ns: aggregate.P50Ns,
			P95Ns: aggregate.P95Ns, P99Ns: aggregate.P99Ns, TotalNs: aggregate.TotalNs,
			Iterations: aggregate.Iterations, SampleCount: aggregate.SampleCount,
			SampleAvgVarianceNs2: aggregate.SampleAvgVarianceNs2, SampleDataVersion: aggregate.SampleDataVersion,
			SummaryVersion: aggregate.SummaryVersion, Samples: aggregate.Samples,
		})
	}
	return parsed, nil
}

func decodeJSInvocation(reader io.Reader) (*jsInvocation, error) {
	data, err := io.ReadAll(io.LimitReader(reader, jsMaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > jsMaxDocumentBytes {
		return nil, fmt.Errorf("stdout exceeds %d bytes", jsMaxDocumentBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document jsInvocation
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("stdout contains more than one JSON value")
		}
		return nil, fmt.Errorf("trailing stdout: %w", err)
	}
	return &document, nil
}

func validateJSIdentity(document *jsInvocation, expected RunMetadata) error {
	if document.SchemaVersion != 1 && document.SchemaVersion != 2 {
		return fmt.Errorf("schema_version = %d, want 1 or 2", document.SchemaVersion)
	}
	if document.SchemaVersion == 1 {
		if expected.JSRuntime != jsbench.RuntimeBun || document.BunVersion != expected.RuntimeVersion {
			return fmt.Errorf("schema 1 is accepted only for legacy Bun retries")
		}
	} else if document.JSRuntime != expected.JSRuntime || document.RuntimeVersion != expected.RuntimeVersion {
		return fmt.Errorf("runtime identity does not match requested runtime")
	}
	if document.SchemaVersion == 2 && ((document.JSRuntime == jsbench.RuntimeNode && document.BunVersion != "") ||
		(document.JSRuntime == jsbench.RuntimeBun && document.BunVersion != "" && document.BunVersion != document.RuntimeVersion)) {
		return fmt.Errorf("bun_version is legacy Bun compatibility data and must not identify another runtime")
	}
	if document.BenchmarkSuite != expected.BenchmarkSuite {
		return fmt.Errorf("benchmark_suite = %q, want %q", document.BenchmarkSuite, expected.BenchmarkSuite)
	}
	if document.ProtocolVersion != expected.ProtocolVersion {
		return fmt.Errorf("protocol_version = %d, want %d", document.ProtocolVersion, expected.ProtocolVersion)
	}
	if document.ZigVersion != expected.ZigVersion {
		return fmt.Errorf("zig_version = %q, want %q", document.ZigVersion, expected.ZigVersion)
	}
	if document.Manifest.Hash != expected.ManifestHash {
		return fmt.Errorf("manifest hash = %q, want canonical %q (bump the pinned manifest digest if the workload change is intentional)",
			document.Manifest.Hash, expected.ManifestHash)
	}
	if document.Manifest.ProtocolVersion != document.ProtocolVersion {
		return fmt.Errorf("manifest protocol_version = %d, document protocol_version = %d", document.Manifest.ProtocolVersion, document.ProtocolVersion)
	}
	measurement := document.Manifest.Measurement
	if err := jsbench.ValidateMeasurement(measurement); err != nil {
		return fmt.Errorf("invalid manifest measurement settings: %w", err)
	}
	if len(document.Manifest.Cases) == 0 {
		return fmt.Errorf("manifest contains no cases")
	}
	seen := map[db.BenchmarkKey]struct{}{}
	for i, manifestCase := range document.Manifest.Cases {
		key := db.BenchmarkKey{Category: manifestCase.Category, Name: manifestCase.Name}
		if key.Category == "" || key.Name == "" || manifestCase.WorkloadVersion <= 0 ||
			manifestCase.WorkloadVersion > jsbench.MaxSafeInteger {
			return fmt.Errorf("manifest case %d has incomplete identity", i)
		}
		if _, err := jsbench.DecodeParameters(manifestCase.Parameters); err != nil {
			return fmt.Errorf("manifest case %d parameters: %w", i, err)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate manifest case %s/%s", key.Category, key.Name)
		}
		seen[key] = struct{}{}
	}
	hash, err := jsbench.ManifestHash(document.Manifest)
	if err != nil {
		return fmt.Errorf("hash manifest: %w", err)
	}
	if document.Manifest.Hash != hash {
		return fmt.Errorf("manifest hash = %q, recomputed %q", document.Manifest.Hash, hash)
	}
	return nil
}

func validateJSResults(document *jsInvocation, cases []jsbench.Case, sampleIndex int64) ([]sample, error) {
	if len(document.Results) != len(cases) {
		return nil, fmt.Errorf("result count = %d, want %d", len(document.Results), len(cases))
	}
	validated := make([]sample, len(cases))
	var processTotalNS int64
	for i, result := range document.Results {
		manifestCase := cases[i]
		if result.Category == "" || result.Name == "" || result.Category != manifestCase.Category || result.Name != manifestCase.Name {
			return nil, fmt.Errorf("result %d does not match ordered manifest case %s/%s", i, manifestCase.Category, manifestCase.Name)
		}
		if result.BatchIterations < *document.Manifest.Measurement.MinBatchIterations ||
			result.BatchIterations > *document.Manifest.Measurement.MaxBatchIterations {
			return nil, fmt.Errorf("%s/%s has invalid batch_iterations", result.Category, result.Name)
		}
		if int64(len(result.BatchElapsedNS)) != document.Manifest.Measurement.MeasuredBatches {
			return nil, fmt.Errorf("%s/%s batch count = %d, want %d", result.Category, result.Name, len(result.BatchElapsedNS), document.Manifest.Measurement.MeasuredBatches)
		}
		if result.InnerRSDPPM == nil {
			return nil, fmt.Errorf("%s/%s is missing inner_rsd_ppm", result.Category, result.Name)
		}
		process, err := recomputeJSProcess(result, sampleIndex, *document.Manifest.Measurement.MaxRSDPPM, *document.Manifest.Measurement.MaxCaseNS)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", result.Category, result.Name, err)
		}
		if processTotalNS > *document.Manifest.Measurement.MaxProcessNS-process.totalNs {
			return nil, fmt.Errorf("measured suite elapsed_ns exceeds max_process_ns %d", *document.Manifest.Measurement.MaxProcessNS)
		}
		processTotalNS += process.totalNs
		validated[i] = process
	}
	return validated, nil
}

func recomputeJSProcess(result jsResult, sampleIndex, maxRSDPPM, maxCaseNS int64) (sample, error) {
	var totalNS int64
	batchMeans := make([]float64, len(result.BatchElapsedNS))
	minMean, maxMean := math.Inf(1), 0.0
	batches := make([]db.ResultSampleBatch, len(result.BatchElapsedNS))
	for i, elapsed := range result.BatchElapsedNS {
		if elapsed <= 0 || elapsed > jsbench.MaxSafeInteger {
			return sample{}, fmt.Errorf("batch %d has invalid elapsed_ns", i)
		}
		var ok bool
		totalNS, ok = addSafe(totalNS, elapsed)
		if !ok {
			return sample{}, fmt.Errorf("total_ns overflow")
		}
		mean := float64(elapsed) / float64(result.BatchIterations)
		if !jsbench.IsPositiveFinite(mean) {
			return sample{}, fmt.Errorf("batch %d has invalid ns/op", i)
		}
		batchMeans[i] = mean
		minMean = math.Min(minMean, mean)
		maxMean = math.Max(maxMean, mean)
		batches[i] = db.ResultSampleBatch{BatchIndex: int64(i), ElapsedNs: elapsed, Iterations: result.BatchIterations}
	}
	if totalNS > maxCaseNS {
		return sample{}, fmt.Errorf("measured elapsed_ns %d exceeds max_case_ns %d", totalNS, maxCaseNS)
	}
	iterations, ok := multiplySafe(result.BatchIterations, int64(len(result.BatchElapsedNS)))
	if !ok {
		return sample{}, fmt.Errorf("iterations overflow")
	}
	avgNS, ok := roundSafe(float64(totalNS) / float64(iterations))
	if !ok || avgNS <= 0 {
		return sample{}, fmt.Errorf("invalid rounded avg_ns")
	}
	minNS, minOK := roundSafe(minMean)
	maxNS, maxOK := roundSafe(maxMean)
	if !minOK || !maxOK || minNS <= 0 || maxNS <= 0 {
		return sample{}, fmt.Errorf("invalid rounded min/max")
	}
	rsdPPM, err := jsbench.CalculateRSDPPM(batchMeans)
	if err != nil {
		return sample{}, err
	}
	if *result.InnerRSDPPM != rsdPPM {
		return sample{}, fmt.Errorf("inner_rsd_ppm = %d, recomputed %d", *result.InnerRSDPPM, rsdPPM)
	}
	if rsdPPM > maxRSDPPM {
		return sample{}, fmt.Errorf("inner_rsd_ppm %d exceeds limit %d", rsdPPM, maxRSDPPM)
	}
	rsd := rsdPPM
	return sample{index: sampleIndex, minNs: minNS, avgNs: avgNS, maxNs: maxNS,
		totalNs: totalNS, iterations: iterations, innerRSDPPM: &rsd, batches: batches}, nil
}

func addSafe(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > jsbench.MaxSafeInteger-b {
		return 0, false
	}
	return a + b, true
}

func multiplySafe(a, b int64) (int64, bool) {
	if a <= 0 || b <= 0 || a > jsbench.MaxSafeInteger/b {
		return 0, false
	}
	return a * b, true
}

func roundSafe(value float64) (int64, bool) {
	if !jsbench.IsPositiveFinite(value) || value > float64(jsbench.MaxSafeInteger) {
		return 0, false
	}
	return int64(math.Floor(value + 0.5)), true
}
