package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"opentui-bench/internal/db"
)

const jsMaxSafeInteger int64 = 1<<53 - 1
const jsMaxDocumentBytes = 10 << 20

type jsInvocation struct {
	SchemaVersion   int64      `json:"schema_version"`
	BenchmarkSuite  string     `json:"benchmark_suite"`
	ProtocolVersion int64      `json:"protocol_version"`
	BunVersion      string     `json:"bun_version"`
	ZigVersion      string     `json:"zig_version"`
	Manifest        jsManifest `json:"manifest"`
	Results         []jsResult `json:"results"`
}

type jsManifest struct {
	Hash            string        `json:"hash"`
	ProtocolVersion int64         `json:"protocol_version"`
	Measurement     jsMeasurement `json:"measurement"`
	Cases           []jsCase      `json:"cases"`
}

type jsMeasurement struct {
	TargetBatchMS      float64 `json:"target_batch_ms"`
	WarmupBatches      int64   `json:"warmup_batches"`
	MeasuredBatches    int64   `json:"measured_batches"`
	MaxRSDPPM          *int64  `json:"max_rsd_ppm"`
	MinBatchIterations *int64  `json:"min_batch_iterations"`
	MaxBatchIterations *int64  `json:"max_batch_iterations"`
	MaxCaseNS          *int64  `json:"max_case_ns"`
	MaxProcessNS       *int64  `json:"max_process_ns"`
}

type jsCase struct {
	Category        string          `json:"category"`
	Name            string          `json:"name"`
	WorkloadVersion int64           `json:"workload_version"`
	Parameters      json.RawMessage `json:"parameters"`
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
	if len(invocations) != 3 {
		return nil, fmt.Errorf("JavaScript protocol requires exactly 3 invocations, got %d", len(invocations))
	}
	if meta.BenchmarkSuite == "" || meta.ProtocolVersion <= 0 || meta.BunVersion == "" ||
		meta.ZigVersion == "" || meta.ManifestHash == "" {
		return nil, fmt.Errorf("complete expected JavaScript identity is required")
	}
	meta.BenchmarkKind = "js"
	meta.SampleCount = 3

	var referenceManifest []byte
	var referenceCases []jsCase
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
		manifestJSON, err := json.Marshal(document.Manifest)
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
	if document.SchemaVersion != 1 {
		return fmt.Errorf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if document.BenchmarkSuite != expected.BenchmarkSuite || document.ProtocolVersion != expected.ProtocolVersion ||
		document.BunVersion != expected.BunVersion || document.ZigVersion != expected.ZigVersion ||
		document.Manifest.Hash != expected.ManifestHash {
		return fmt.Errorf("identity does not match requested suite, protocol, versions, and manifest")
	}
	if document.Manifest.ProtocolVersion != document.ProtocolVersion {
		return fmt.Errorf("manifest protocol_version = %d, document protocol_version = %d", document.Manifest.ProtocolVersion, document.ProtocolVersion)
	}
	measurement := document.Manifest.Measurement
	if !isPositiveFinite(measurement.TargetBatchMS) ||
		measurement.WarmupBatches <= 0 || measurement.WarmupBatches > jsMaxSafeInteger ||
		measurement.MeasuredBatches < 2 || measurement.MeasuredBatches > jsMaxSafeInteger ||
		measurement.MaxRSDPPM == nil || *measurement.MaxRSDPPM < 0 || *measurement.MaxRSDPPM > jsMaxSafeInteger ||
		measurement.MinBatchIterations == nil || *measurement.MinBatchIterations <= 0 || *measurement.MinBatchIterations > jsMaxSafeInteger ||
		measurement.MaxBatchIterations == nil || *measurement.MaxBatchIterations < *measurement.MinBatchIterations || *measurement.MaxBatchIterations > jsMaxSafeInteger ||
		measurement.MaxCaseNS == nil || *measurement.MaxCaseNS <= 0 || *measurement.MaxCaseNS > jsMaxSafeInteger ||
		measurement.MaxProcessNS == nil || *measurement.MaxProcessNS <= 0 || *measurement.MaxProcessNS > jsMaxSafeInteger {
		return fmt.Errorf("invalid manifest measurement settings")
	}
	if len(document.Manifest.Cases) == 0 {
		return fmt.Errorf("manifest contains no cases")
	}
	seen := map[db.BenchmarkKey]struct{}{}
	for i, manifestCase := range document.Manifest.Cases {
		key := db.BenchmarkKey{Category: manifestCase.Category, Name: manifestCase.Name}
		if key.Category == "" || key.Name == "" || manifestCase.WorkloadVersion <= 0 ||
			manifestCase.WorkloadVersion > jsMaxSafeInteger {
			return fmt.Errorf("manifest case %d has incomplete identity", i)
		}
		if _, err := decodeJSParameters(manifestCase.Parameters); err != nil {
			return fmt.Errorf("manifest case %d parameters: %w", i, err)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate manifest case %s/%s", key.Category, key.Name)
		}
		seen[key] = struct{}{}
	}
	hash, err := jsManifestHash(document.Manifest)
	if err != nil {
		return fmt.Errorf("hash manifest: %w", err)
	}
	if !validSHA256(document.Manifest.Hash) || document.Manifest.Hash != hash {
		return fmt.Errorf("manifest hash = %q, recomputed %q", document.Manifest.Hash, hash)
	}
	return nil
}

func jsManifestHash(manifest jsManifest) (string, error) {
	cases := make([]any, len(manifest.Cases))
	for i, manifestCase := range manifest.Cases {
		parameters, err := decodeJSParameters(manifestCase.Parameters)
		if err != nil {
			return "", err
		}
		cases[i] = map[string]any{
			"category": manifestCase.Category, "name": manifestCase.Name,
			"workload_version": manifestCase.WorkloadVersion, "parameters": parameters,
		}
	}
	value := map[string]any{
		"protocol_version": manifest.ProtocolVersion,
		"measurement": map[string]any{
			"target_batch_ms":      manifest.Measurement.TargetBatchMS,
			"warmup_batches":       manifest.Measurement.WarmupBatches,
			"measured_batches":     manifest.Measurement.MeasuredBatches,
			"max_rsd_ppm":          *manifest.Measurement.MaxRSDPPM,
			"min_batch_iterations": *manifest.Measurement.MinBatchIterations,
			"max_batch_iterations": *manifest.Measurement.MaxBatchIterations,
			"max_case_ns":          *manifest.Measurement.MaxCaseNS,
			"max_process_ns":       *manifest.Measurement.MaxProcessNS,
		},
		"cases": cases,
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'}))
	return fmt.Sprintf("sha256:%x", digest), nil
}

func decodeJSParameters(data json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parameters map[string]any
	if err := decoder.Decode(&parameters); err != nil {
		return nil, fmt.Errorf("must be an object: %w", err)
	}
	if parameters == nil {
		return nil, fmt.Errorf("must be an object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("must contain exactly one object")
	}
	for key, value := range parameters {
		switch value := value.(type) {
		case string, bool:
		case json.Number:
			number, err := value.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("%q is not a finite number", key)
			}
			parameters[key] = number
		default:
			return nil, fmt.Errorf("%q must be a string, number, or boolean", key)
		}
	}
	return parameters, nil
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validateJSResults(document *jsInvocation, cases []jsCase, sampleIndex int64) ([]sample, error) {
	if len(document.Results) != len(cases) {
		return nil, fmt.Errorf("result count = %d, want %d", len(document.Results), len(cases))
	}
	validated := make([]sample, len(cases))
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
		process, err := recomputeJSProcess(result, sampleIndex, *document.Manifest.Measurement.MaxRSDPPM)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", result.Category, result.Name, err)
		}
		validated[i] = process
	}
	return validated, nil
}

func recomputeJSProcess(result jsResult, sampleIndex, maxRSDPPM int64) (sample, error) {
	var totalNS int64
	batchMeans := make([]float64, len(result.BatchElapsedNS))
	minMean, maxMean := math.Inf(1), 0.0
	batches := make([]db.ResultSampleBatch, len(result.BatchElapsedNS))
	for i, elapsed := range result.BatchElapsedNS {
		if elapsed <= 0 || elapsed > jsMaxSafeInteger {
			return sample{}, fmt.Errorf("batch %d has invalid elapsed_ns", i)
		}
		var ok bool
		totalNS, ok = addSafe(totalNS, elapsed)
		if !ok {
			return sample{}, fmt.Errorf("total_ns overflow")
		}
		mean := float64(elapsed) / float64(result.BatchIterations)
		if !isPositiveFinite(mean) {
			return sample{}, fmt.Errorf("batch %d has invalid ns/op", i)
		}
		batchMeans[i] = mean
		minMean = math.Min(minMean, mean)
		maxMean = math.Max(maxMean, mean)
		batches[i] = db.ResultSampleBatch{BatchIndex: int64(i), ElapsedNs: elapsed, Iterations: result.BatchIterations}
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
	rsdPPM, err := calculateRSDPPM(batchMeans)
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

func calculateRSDPPM(values []float64) (int64, error) {
	if len(values) < 2 {
		return 0, fmt.Errorf("at least two measured batches are required")
	}
	var sum float64
	for _, value := range values {
		if !isPositiveFinite(value) {
			return 0, fmt.Errorf("invalid batch timing")
		}
		sum += value
	}
	mean := sum / float64(len(values))
	var sumSquares float64
	for _, value := range values {
		delta := value - mean
		sumSquares += delta * delta
	}
	rsd := math.Sqrt(sumSquares/float64(len(values)-1)) / math.Abs(mean)
	value, ok := roundNonnegativeSafe(rsd * 1_000_000)
	if !ok || value < 0 {
		return 0, fmt.Errorf("invalid RSD")
	}
	return value, nil
}

func addSafe(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > jsMaxSafeInteger-b {
		return 0, false
	}
	return a + b, true
}

func multiplySafe(a, b int64) (int64, bool) {
	if a <= 0 || b <= 0 || a > jsMaxSafeInteger/b {
		return 0, false
	}
	return a * b, true
}

func roundSafe(value float64) (int64, bool) {
	if !isPositiveFinite(value) || value > float64(jsMaxSafeInteger) {
		return 0, false
	}
	return int64(math.Floor(value + 0.5)), true
}

func roundNonnegativeSafe(value float64) (int64, bool) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(jsMaxSafeInteger) {
		return 0, false
	}
	return int64(math.Floor(value + 0.5)), true
}

func isPositiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
