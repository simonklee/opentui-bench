package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"

	"opentui-bench/internal/db"
)

const jsMaxSafeInteger = int64(1<<53 - 1)

type createRunResult struct {
	Category             string            `json:"category"`
	Name                 string            `json:"name"`
	MinNs                int64             `json:"min_ns"`
	AvgNs                int64             `json:"avg_ns"`
	MaxNs                int64             `json:"max_ns"`
	StdDevNs             int64             `json:"std_dev_ns"`
	P50Ns                int64             `json:"p50_ns"`
	P95Ns                int64             `json:"p95_ns"`
	P99Ns                int64             `json:"p99_ns"`
	TotalNs              int64             `json:"total_ns"`
	Iterations           int64             `json:"iterations"`
	SampleCount          int64             `json:"sample_count"`
	SampleAvgVarianceNs2 *float64          `json:"sample_avg_variance_ns2"`
	SampleDataVersion    *int64            `json:"sample_data_version"`
	SummaryVersion       *int64            `json:"summary_version"`
	Samples              []db.ResultSample `json:"samples"`
	MemStats             []struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	} `json:"mem_stats,omitempty"`
}

type jsStoredManifest struct {
	Hash            string `json:"hash"`
	ProtocolVersion int64  `json:"protocol_version"`
	Measurement     struct {
		TargetBatchMS      float64 `json:"target_batch_ms"`
		WarmupBatches      int64   `json:"warmup_batches"`
		MeasuredBatches    int64   `json:"measured_batches"`
		MaxRSDPPM          *int64  `json:"max_rsd_ppm"`
		MinBatchIterations int64   `json:"min_batch_iterations"`
		MaxBatchIterations int64   `json:"max_batch_iterations"`
		MaxCaseNS          int64   `json:"max_case_ns"`
		MaxProcessNS       int64   `json:"max_process_ns"`
	} `json:"measurement"`
	Cases []struct {
		Category        string          `json:"category"`
		Name            string          `json:"name"`
		WorkloadVersion int64           `json:"workload_version"`
		Parameters      json.RawMessage `json:"parameters"`
	} `json:"cases"`
}

func validateCreateRun(run *db.Run, results []createRunResult) error {
	if run.BenchmarkKind != "zig" && run.BenchmarkKind != canonicalJSKind {
		return fmt.Errorf("benchmark_kind must be 'zig' or 'js'")
	}
	if run.BenchmarkKind == "zig" {
		if run.BunVersion != "" || run.ManifestHash != "" || run.ManifestJSON != "" {
			return fmt.Errorf("Zig runs must not include Bun or manifest fields")
		}
		return validateZigStoredResults(results)
	}
	if run.BenchmarkSuite != canonicalJSSuite || run.ProtocolVersion != canonicalJSProtocol ||
		run.BunVersion != canonicalJSBunVersion || run.ZigVersion != canonicalJSZigVersion ||
		run.ManifestHash != canonicalJSManifestHash {
		return fmt.Errorf("JavaScript runs require canonical suite, protocol, Bun, Zig, and manifest identity")
	}
	if run.ManifestJSON == "" {
		return fmt.Errorf("manifest_json is required for JavaScript runs")
	}
	var manifest jsStoredManifest
	decoder := json.NewDecoder(bytes.NewReader([]byte(run.ManifestJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("invalid manifest_json: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid manifest_json: must contain exactly one JSON value")
	}
	if manifest.Hash != run.ManifestHash || manifest.ProtocolVersion != run.ProtocolVersion ||
		!isPositiveFinite(manifest.Measurement.TargetBatchMS) || manifest.Measurement.WarmupBatches <= 0 ||
		manifest.Measurement.MeasuredBatches < 2 || manifest.Measurement.MaxRSDPPM == nil || *manifest.Measurement.MaxRSDPPM < 0 ||
		manifest.Measurement.MinBatchIterations <= 0 || manifest.Measurement.MaxBatchIterations < manifest.Measurement.MinBatchIterations ||
		manifest.Measurement.MaxCaseNS <= 0 || manifest.Measurement.MaxProcessNS < manifest.Measurement.MaxCaseNS {
		return fmt.Errorf("manifest_json identity or measurement settings are inconsistent")
	}
	recomputed, err := storedManifestHash(manifest)
	if err != nil {
		return fmt.Errorf("hash manifest_json: %w", err)
	}
	if recomputed != manifest.Hash {
		return fmt.Errorf("manifest hash = %q, recomputed %q", manifest.Hash, recomputed)
	}
	if len(results) == 0 || len(results) != len(manifest.Cases) {
		return fmt.Errorf("JavaScript results must exactly match the manifest cases")
	}
	seen := map[db.BenchmarkKey]bool{}
	for i := range results {
		key := db.BenchmarkKey{Category: manifest.Cases[i].Category, Name: manifest.Cases[i].Name}
		if key.Category == "" || key.Name == "" || manifest.Cases[i].WorkloadVersion <= 0 || seen[key] {
			return fmt.Errorf("manifest case %d has invalid or duplicate identity", i)
		}
		if _, err := decodeStoredJSParameters(manifest.Cases[i].Parameters); err != nil {
			return fmt.Errorf("manifest case %d parameters: %w", i, err)
		}
		seen[key] = true
		if results[i].Category != manifest.Cases[i].Category || results[i].Name != manifest.Cases[i].Name {
			return fmt.Errorf("result %d does not match ordered manifest case", i)
		}
		if len(results[i].MemStats) != 0 {
			return fmt.Errorf("JavaScript v1 does not accept memory statistics")
		}
		if err := validateJSStoredResult(&results[i], manifest.Measurement.MeasuredBatches, *manifest.Measurement.MaxRSDPPM); err != nil {
			return fmt.Errorf("%s/%s: %w", results[i].Category, results[i].Name, err)
		}
	}
	return nil
}

func validateZigStoredResults(results []createRunResult) error {
	for i := range results {
		result := &results[i]
		if result.Category == "" || result.Name == "" || result.Iterations < 0 || result.SampleCount < 0 ||
			result.MinNs < 0 || result.AvgNs < 0 || result.MaxNs < 0 || result.StdDevNs < 0 ||
			result.P50Ns < 0 || result.P95Ns < 0 || result.P99Ns < 0 || result.TotalNs < 0 {
			return fmt.Errorf("Zig result %d has invalid identity or negative values", i)
		}
		untimed := result.MinNs == 0 && result.AvgNs == 0 && result.MaxNs == 0 && result.StdDevNs == 0 &&
			result.P50Ns == 0 && result.P95Ns == 0 && result.P99Ns == 0 && result.TotalNs == 0
		if untimed {
			continue
		}
		if result.MinNs <= 0 || result.AvgNs <= 0 || result.MaxNs <= 0 || result.TotalNs <= 0 ||
			result.Iterations <= 0 || result.SampleCount <= 0 || result.MinNs > result.AvgNs || result.AvgNs > result.MaxNs {
			return fmt.Errorf("Zig result %d must use a valid timed form or an all-zero untimed form", i)
		}
		if result.P50Ns != 0 || result.P95Ns != 0 || result.P99Ns != 0 {
			if result.P50Ns < result.MinNs || result.P50Ns > result.P95Ns || result.P95Ns > result.P99Ns || result.P99Ns > result.MaxNs {
				return fmt.Errorf("Zig result %d has invalid percentiles", i)
			}
		}
	}
	return nil
}

func storedManifestHash(manifest jsStoredManifest) (string, error) {
	cases := make([]any, len(manifest.Cases))
	for i, manifestCase := range manifest.Cases {
		parameters, err := decodeStoredJSParameters(manifestCase.Parameters)
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
			"target_batch_ms": manifest.Measurement.TargetBatchMS, "warmup_batches": manifest.Measurement.WarmupBatches,
			"measured_batches": manifest.Measurement.MeasuredBatches, "max_rsd_ppm": *manifest.Measurement.MaxRSDPPM,
			"min_batch_iterations": manifest.Measurement.MinBatchIterations,
			"max_batch_iterations": manifest.Measurement.MaxBatchIterations,
			"max_case_ns": manifest.Measurement.MaxCaseNS, "max_process_ns": manifest.Measurement.MaxProcessNS,
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

func decodeStoredJSParameters(data json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parameters map[string]any
	if err := decoder.Decode(&parameters); err != nil || parameters == nil {
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

func isPositiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateJSStoredResult(result *createRunResult, measuredBatches, maxRSDPPM int64) error {
	if result.Category == "" || result.Name == "" || result.SampleCount != 3 || len(result.Samples) != 3 {
		return fmt.Errorf("JavaScript results require exactly 3 process samples")
	}
	avgs := make([]int64, 3)
	var totalNs, iterations, avgSum int64
	minNs, maxNs := int64(math.MaxInt64), int64(0)
	for i := range result.Samples {
		sample := &result.Samples[i]
		if sample.SampleIndex != int64(i) || sample.AvgNs <= 0 || sample.InnerRSDPPM == nil || *sample.InnerRSDPPM < 0 || *sample.InnerRSDPPM > maxRSDPPM {
			return fmt.Errorf("sample %d has invalid index, timing, or inner_rsd_ppm", i)
		}
		if int64(len(sample.Batches)) != measuredBatches {
			return fmt.Errorf("sample %d has %d batches, want %d", i, len(sample.Batches), measuredBatches)
		}
		means := make([]float64, len(sample.Batches))
		var sampleTotal, sampleIterations int64
		var batchIterations int64
		processMin, processMax := math.Inf(1), 0.0
		for batchIndex, batch := range sample.Batches {
			if batch.BatchIndex != int64(batchIndex) || batch.ElapsedNs <= 0 || batch.ElapsedNs > jsMaxSafeInteger ||
				batch.Iterations <= 0 || batch.Iterations > jsMaxSafeInteger ||
				sampleTotal > jsMaxSafeInteger-batch.ElapsedNs || sampleIterations > jsMaxSafeInteger-batch.Iterations {
				return fmt.Errorf("sample %d batch %d has invalid evidence", i, batchIndex)
			}
			if batchIndex == 0 {
				batchIterations = batch.Iterations
			} else if batch.Iterations != batchIterations {
				return fmt.Errorf("sample %d batch iterations are inconsistent", i)
			}
			sampleTotal += batch.ElapsedNs
			sampleIterations += batch.Iterations
			mean := float64(batch.ElapsedNs) / float64(batch.Iterations)
			means[batchIndex] = mean
			processMin = math.Min(processMin, mean)
			processMax = math.Max(processMax, mean)
		}
		if sample.AvgNs != roundPositive(float64(sampleTotal)/float64(sampleIterations)) {
			return fmt.Errorf("sample %d avg_ns is inconsistent with batch evidence", i)
		}
		if *sample.InnerRSDPPM != calculateStoredRSDPPM(means) {
			return fmt.Errorf("sample %d inner_rsd_ppm is inconsistent with batch evidence", i)
		}
		if totalNs > jsMaxSafeInteger-sampleTotal || iterations > jsMaxSafeInteger-sampleIterations || avgSum > jsMaxSafeInteger-sample.AvgNs {
			return fmt.Errorf("aggregate timing overflow")
		}
		totalNs += sampleTotal
		iterations += sampleIterations
		avgSum += sample.AvgNs
		minNs = min64(minNs, roundPositive(processMin))
		maxNs = max64(maxNs, roundPositive(processMax))
		avgs[i] = sample.AvgNs
	}
	variance := sampleVariance3(avgs)
	if result.MinNs != minNs || result.AvgNs != avgSum/3 || result.MaxNs != maxNs ||
		result.StdDevNs != int64(math.Sqrt(variance)) || result.P50Ns != percentileStored(avgs, .5) ||
		result.P95Ns != percentileStored(avgs, .95) || result.P99Ns != percentileStored(avgs, .99) ||
		result.TotalNs != totalNs || result.Iterations != iterations || result.SampleAvgVarianceNs2 == nil ||
		*result.SampleAvgVarianceNs2 != variance || result.SampleDataVersion == nil || *result.SampleDataVersion != db.CurrentSampleDataVersion ||
		result.SummaryVersion == nil || *result.SummaryVersion != db.CurrentSummaryVersion {
		return fmt.Errorf("aggregate summary is inconsistent with process samples")
	}
	return nil
}

func calculateStoredRSDPPM(values []float64) int64 {
	var mean, sumSquares float64
	for i, value := range values {
		delta := value - mean
		mean += delta / float64(i+1)
		sumSquares += delta * (value - mean)
	}
	return int64(math.Floor((math.Sqrt(sumSquares/float64(len(values)-1))/math.Abs(mean))*1_000_000 + .5))
}

func sampleVariance3(values []int64) float64 {
	var mean, sumSquares float64
	for i, value := range values {
		delta := float64(value) - mean
		mean += delta / float64(i+1)
		sumSquares += delta * (float64(value) - mean)
	}
	return sumSquares / float64(len(values)-1)
}

func percentileStored(values []int64, p float64) int64 {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	position := p * float64(len(sorted)-1)
	lower := int(position)
	if lower+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	fraction := position - float64(lower)
	return int64(float64(sorted[lower])*(1-fraction) + float64(sorted[lower+1])*fraction)
}

func roundPositive(value float64) int64 { return int64(math.Floor(value + .5)) }
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
