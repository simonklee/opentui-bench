package web

import (
	"fmt"
	"math"
	"sort"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
)

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

func validateCreateRun(run *db.Run, results []createRunResult) error {
	if run.BenchmarkKind != "zig" && run.BenchmarkKind != jsbench.Kind {
		return fmt.Errorf("benchmark_kind must be 'zig' or 'js'")
	}
	if run.BenchmarkKind == "zig" {
		if run.BunVersion != "" || run.JSRuntime != "" || run.RuntimeVersion != "" || run.ManifestHash != "" || run.ManifestJSON != "" {
			return fmt.Errorf("Zig runs must not include Bun or manifest fields")
		}
		return validateZigStoredResults(results)
	}
	if !jsbench.MatchesIdentity(run.BenchmarkSuite, run.ProtocolVersion, run.JSRuntime, run.RuntimeVersion, run.ZigVersion, run.ManifestHash) {
		return fmt.Errorf("JavaScript runs require canonical suite, protocol, runtime, Zig, and manifest identity")
	}
	if (run.JSRuntime == jsbench.RuntimeNode && run.BunVersion != "") ||
		(run.JSRuntime == jsbench.RuntimeBun && run.BunVersion != run.RuntimeVersion) {
		return fmt.Errorf("bun_version is legacy compatibility storage for Bun only")
	}
	if run.ManifestJSON == "" {
		return fmt.Errorf("manifest_json is required for JavaScript runs")
	}
	manifest, err := jsbench.DecodeManifest([]byte(run.ManifestJSON))
	if err != nil {
		return fmt.Errorf("invalid manifest_json: %w", err)
	}
	if manifest.Hash != run.ManifestHash || manifest.ProtocolVersion != run.ProtocolVersion ||
		jsbench.ValidateMeasurement(manifest.Measurement) != nil ||
		*manifest.Measurement.MaxProcessNS < *manifest.Measurement.MaxCaseNS {
		return fmt.Errorf("manifest_json identity or measurement settings are inconsistent")
	}
	recomputed, err := jsbench.ManifestHash(manifest)
	if err != nil {
		return fmt.Errorf("hash manifest_json: %w", err)
	}
	if recomputed != manifest.Hash {
		return fmt.Errorf("manifest hash = %q, recomputed %q", manifest.Hash, recomputed)
	}
	canonicalManifest, err := jsbench.CanonicalManifestJSON(manifest)
	if err != nil {
		return fmt.Errorf("canonicalize manifest_json: %w", err)
	}
	run.ManifestJSON = string(canonicalManifest)
	if len(results) == 0 || len(results) != len(manifest.Cases) {
		return fmt.Errorf("JavaScript results must exactly match the manifest cases")
	}
	seen := map[db.BenchmarkKey]bool{}
	var processTotals [jsbench.Samples]int64
	for i := range results {
		key := db.BenchmarkKey{Category: manifest.Cases[i].Category, Name: manifest.Cases[i].Name}
		if key.Category == "" || key.Name == "" || manifest.Cases[i].WorkloadVersion <= 0 || seen[key] {
			return fmt.Errorf("manifest case %d has invalid or duplicate identity", i)
		}
		if _, err := jsbench.DecodeParameters(manifest.Cases[i].Parameters); err != nil {
			return fmt.Errorf("manifest case %d parameters: %w", i, err)
		}
		seen[key] = true
		if results[i].Category != manifest.Cases[i].Category || results[i].Name != manifest.Cases[i].Name {
			return fmt.Errorf("result %d does not match ordered manifest case", i)
		}
		if len(results[i].MemStats) != 0 {
			return fmt.Errorf("JavaScript v1 does not accept memory statistics")
		}
		if err := validateJSStoredResult(&results[i], manifest.Measurement, &processTotals); err != nil {
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

func validateJSStoredResult(result *createRunResult, measurement jsbench.Measurement, processTotals *[jsbench.Samples]int64) error {
	if result.Category == "" || result.Name == "" || result.SampleCount != jsbench.Samples || len(result.Samples) != jsbench.Samples {
		return fmt.Errorf("JavaScript results require exactly 3 process samples")
	}
	avgs := make([]int64, jsbench.Samples)
	var totalNs, iterations, avgSum int64
	minNs, maxNs := int64(math.MaxInt64), int64(0)
	for i := range result.Samples {
		sample := &result.Samples[i]
		if sample.SampleIndex != int64(i) || sample.AvgNs <= 0 || sample.InnerRSDPPM == nil || *sample.InnerRSDPPM < 0 || *sample.InnerRSDPPM > *measurement.MaxRSDPPM {
			return fmt.Errorf("sample %d has invalid index, timing, or inner_rsd_ppm", i)
		}
		if int64(len(sample.Batches)) != measurement.MeasuredBatches {
			return fmt.Errorf("sample %d has %d batches, want %d", i, len(sample.Batches), measurement.MeasuredBatches)
		}
		means := make([]float64, len(sample.Batches))
		var sampleTotal, sampleIterations int64
		var batchIterations int64
		processMin, processMax := math.Inf(1), 0.0
		for batchIndex, batch := range sample.Batches {
			if batch.BatchIndex != int64(batchIndex) || batch.ElapsedNs <= 0 || batch.ElapsedNs > jsbench.MaxSafeInteger ||
				batch.Iterations < *measurement.MinBatchIterations || batch.Iterations > *measurement.MaxBatchIterations ||
				sampleTotal > jsbench.MaxSafeInteger-batch.ElapsedNs || sampleIterations > jsbench.MaxSafeInteger-batch.Iterations {
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
		if sampleTotal > *measurement.MaxCaseNS {
			return fmt.Errorf("sample %d measured elapsed_ns exceeds max_case_ns", i)
		}
		if processTotals[i] > *measurement.MaxProcessNS-sampleTotal {
			return fmt.Errorf("sample %d suite measured elapsed_ns exceeds max_process_ns", i)
		}
		processTotals[i] += sampleTotal
		if sample.AvgNs != roundPositive(float64(sampleTotal)/float64(sampleIterations)) {
			return fmt.Errorf("sample %d avg_ns is inconsistent with batch evidence", i)
		}
		rsdPPM, err := jsbench.CalculateRSDPPM(means)
		if err != nil || *sample.InnerRSDPPM != rsdPPM {
			return fmt.Errorf("sample %d inner_rsd_ppm is inconsistent with batch evidence", i)
		}
		if totalNs > jsbench.MaxSafeInteger-sampleTotal || iterations > jsbench.MaxSafeInteger-sampleIterations || avgSum > jsbench.MaxSafeInteger-sample.AvgNs {
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
	if result.MinNs != minNs || result.AvgNs != avgSum/jsbench.Samples || result.MaxNs != maxNs ||
		result.StdDevNs != int64(math.Sqrt(variance)) || result.P50Ns != percentileStored(avgs, .5) ||
		result.P95Ns != percentileStored(avgs, .95) || result.P99Ns != percentileStored(avgs, .99) ||
		result.TotalNs != totalNs || result.Iterations != iterations || result.SampleAvgVarianceNs2 == nil ||
		*result.SampleAvgVarianceNs2 != variance || result.SampleDataVersion == nil || *result.SampleDataVersion != db.CurrentSampleDataVersion ||
		result.SummaryVersion == nil || *result.SummaryVersion != db.CurrentSummaryVersion {
		return fmt.Errorf("aggregate summary is inconsistent with process samples")
	}
	return nil
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
