package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"opentui-bench/internal/db"
)

type BenchmarkJSON struct {
	Benchmark string       `json:"benchmark"`
	Results   []ResultJSON `json:"results"`
}

type ResultJSON struct {
	Name       string        `json:"name"`
	MinNs      int64         `json:"min_ns"`
	AvgNs      int64         `json:"avg_ns"`
	MaxNs      int64         `json:"max_ns"`
	TotalNs    int64         `json:"total_ns"`
	Iterations int64         `json:"iterations"`
	MemStats   []MemStatJSON `json:"mem_stats,omitempty"`
}

type MemStatJSON struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type RunMetadata struct {
	CommitHash      string
	CommitHashFull  string
	CommitMessage   string
	CommitDate      string
	Branch          string
	MachineID       string
	Notes           string
	ZigOptimize     string
	SampleCount     int
	BenchmarkKind   string
	BenchmarkSuite  string
	ProtocolVersion int64
	BunVersion      string
	ZigVersion      string
	ManifestHash    string
	ManifestJSON    string
}

// ParsedRun contains a fully parsed and aggregated benchmark run, ready for
// storage (local DB or remote API). Built entirely in memory with no side effects.
type ParsedRun struct {
	Meta    RunMetadata
	Results []ParsedResult
}

// ParsedResult is a single aggregated benchmark result with optional memory stats.
type ParsedResult struct {
	Category             string
	Name                 string
	MinNs                int64
	AvgNs                int64
	MaxNs                int64
	StdDevNs             int64
	P50Ns                int64
	P95Ns                int64
	P99Ns                int64
	TotalNs              int64
	Iterations           int64
	SampleCount          int64
	SampleAvgVarianceNs2 *float64
	SampleDataVersion    int64
	SummaryVersion       int64
	Samples              []db.ResultSample
	MemStats             []MemStatJSON
}

type sample struct {
	index       int64
	minNs       int64
	avgNs       int64
	maxNs       int64
	totalNs     int64
	iterations  int64
	memStats    []MemStatJSON
	innerRSDPPM *int64
	batches     []db.ResultSampleBatch
}

// Parse reads benchmark JSON output and aggregates samples into a ParsedRun.
// It does everything Record() does up to the DB write: parses JSON, aggregates
// samples, builds the result structs. No DB dependency. No side effects.
func Parse(reader io.Reader, meta RunMetadata) (*ParsedRun, error) {
	return ParseInvocations([]io.Reader{reader}, meta)
}

// ParseInvocations preserves process invocation boundaries. A benchmark absent
// from one invocation has a gap in sample_index rather than shifting later
// samples or being paired by output row position.
func ParseInvocations(invocations []io.Reader, meta RunMetadata) (*ParsedRun, error) {
	if meta.ZigOptimize == "" {
		meta.ZigOptimize = "ReleaseFast"
	}

	samples := make(map[db.BenchmarkKey][]sample)
	keyOrder := []db.BenchmarkKey{}
	for invocationIndex, reader := range invocations {
		invocation, order, err := parseInvocation(reader)
		if err != nil {
			return nil, fmt.Errorf("parse invocation %d: %w", invocationIndex, err)
		}
		for _, key := range order {
			if _, exists := samples[key]; !exists {
				keyOrder = append(keyOrder, key)
			}
			s := invocation[key]
			s.index = int64(invocationIndex)
			samples[key] = append(samples[key], s)
		}
	}

	parsed := &ParsedRun{Meta: meta}
	for _, key := range keyOrder {
		sampleList := samples[key]
		agg := aggregateSamples(key.Category, key.Name, sampleList)
		var memStats []MemStatJSON
		for _, s := range sampleList {
			if len(s.memStats) > 0 {
				memStats = s.memStats
				break
			}
		}
		parsed.Results = append(parsed.Results, ParsedResult{
			Category: agg.Category, Name: agg.Name, MinNs: agg.MinNs, AvgNs: agg.AvgNs,
			MaxNs: agg.MaxNs, StdDevNs: agg.StdDevNs, P50Ns: agg.P50Ns, P95Ns: agg.P95Ns,
			P99Ns: agg.P99Ns, TotalNs: agg.TotalNs, Iterations: agg.Iterations,
			SampleCount: agg.SampleCount, SampleAvgVarianceNs2: agg.SampleAvgVarianceNs2,
			SampleDataVersion: agg.SampleDataVersion, SummaryVersion: agg.SummaryVersion,
			Samples: agg.Samples, MemStats: memStats,
		})
	}
	return parsed, nil
}

func parseInvocation(reader io.Reader) (map[db.BenchmarkKey]sample, []db.BenchmarkKey, error) {
	results := make(map[db.BenchmarkKey]sample)
	var order []db.BenchmarkKey

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "Memory stats enabled" {
			continue
		}
		if trimmed[0] != '{' {
			continue
		}

		var bench BenchmarkJSON
		if err := json.Unmarshal([]byte(trimmed), &bench); err != nil {
			return nil, nil, fmt.Errorf("parse benchmark JSON on line %d: %w", lineNum, err)
		}

		for _, r := range bench.Results {
			key := db.BenchmarkKey{Category: bench.Benchmark, Name: r.Name}
			if _, exists := results[key]; exists {
				return nil, nil, fmt.Errorf("duplicate benchmark %s/%s on line %d", key.Category, key.Name, lineNum)
			}
			order = append(order, key)
			results[key] = sample{
				minNs:      r.MinNs,
				avgNs:      r.AvgNs,
				maxNs:      r.MaxNs,
				totalNs:    r.TotalNs,
				iterations: r.Iterations,
				memStats:   r.MemStats,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan input: %w", err)
	}
	return results, order, nil
}

// Store writes a ParsedRun to the database. Returns the run ID and result count.
func Store(database *db.DB, parsed *ParsedRun) (int64, int, error) {
	run := &db.Run{
		CommitHash:      parsed.Meta.CommitHash,
		CommitHashFull:  parsed.Meta.CommitHashFull,
		CommitMessage:   parsed.Meta.CommitMessage,
		CommitDate:      parsed.Meta.CommitDate,
		Branch:          parsed.Meta.Branch,
		RunDate:         time.Now().UTC().Format(time.RFC3339),
		MachineID:       parsed.Meta.MachineID,
		Notes:           parsed.Meta.Notes,
		ZigOptimize:     parsed.Meta.ZigOptimize,
		BenchmarkKind:   parsed.Meta.BenchmarkKind,
		BenchmarkSuite:  parsed.Meta.BenchmarkSuite,
		ProtocolVersion: parsed.Meta.ProtocolVersion,
		BunVersion:      parsed.Meta.BunVersion,
		ZigVersion:      parsed.Meta.ZigVersion,
		ManifestHash:    parsed.Meta.ManifestHash,
		ManifestJSON:    parsed.Meta.ManifestJSON,
	}

	if run.ZigOptimize == "" {
		run.ZigOptimize = "ReleaseFast"
	}

	results := make([]db.Result, 0, len(parsed.Results))
	for _, pr := range parsed.Results {
		result := db.Result{
			Category:             pr.Category,
			Name:                 pr.Name,
			MinNs:                pr.MinNs,
			AvgNs:                pr.AvgNs,
			MaxNs:                pr.MaxNs,
			StdDevNs:             pr.StdDevNs,
			P50Ns:                pr.P50Ns,
			P95Ns:                pr.P95Ns,
			P99Ns:                pr.P99Ns,
			TotalNs:              pr.TotalNs,
			Iterations:           pr.Iterations,
			SampleCount:          pr.SampleCount,
			SampleAvgVarianceNs2: pr.SampleAvgVarianceNs2,
			SampleDataVersion:    pr.SampleDataVersion,
			SummaryVersion:       pr.SummaryVersion,
			Samples:              pr.Samples,
		}
		for _, ms := range pr.MemStats {
			result.MemStats = append(result.MemStats, db.MemStat{
				StatName: ms.Name,
				Bytes:    ms.Bytes,
			})
		}
		results = append(results, result)
	}
	runID, _, err := database.InsertRunWithResults(run, results)
	if err != nil {
		return 0, 0, fmt.Errorf("insert run: %w", err)
	}
	return runID, len(parsed.Results), nil
}

// Record parses benchmark output and writes it to the database. This is a
// convenience function that calls Parse() then Store(). Existing callers
// (backfill, local usage) don't need to change.
func Record(database *db.DB, reader io.Reader, meta RunMetadata) (int64, int, error) {
	parsed, err := Parse(reader, meta)
	if err != nil {
		return 0, 0, err
	}
	return Store(database, parsed)
}

func aggregateSamples(category, name string, sampleList []sample) *db.Result {
	n := len(sampleList)
	if n == 0 {
		return &db.Result{
			Category:    category,
			Name:        name,
			SampleCount: 0,
		}
	}

	untimed := true
	for _, s := range sampleList {
		if s.minNs != 0 || s.avgNs != 0 || s.maxNs != 0 || s.totalNs != 0 {
			untimed = false
			break
		}
	}
	if untimed {
		var iterations int64
		for _, s := range sampleList {
			iterations += s.iterations
		}
		return &db.Result{
			Category:       category,
			Name:           name,
			Iterations:     iterations,
			SampleCount:    int64(n),
			SummaryVersion: db.CurrentSummaryVersion,
		}
	}

	if n == 1 {
		s := sampleList[0]
		return &db.Result{
			Category:          category,
			Name:              name,
			MinNs:             s.minNs,
			AvgNs:             s.avgNs,
			MaxNs:             s.maxNs,
			StdDevNs:          0,
			P50Ns:             s.avgNs,
			P95Ns:             s.avgNs,
			P99Ns:             s.avgNs,
			TotalNs:           s.totalNs,
			Iterations:        s.iterations,
			SampleCount:       1,
			SampleDataVersion: db.CurrentSampleDataVersion,
			SummaryVersion:    db.CurrentSummaryVersion,
			Samples:           []db.ResultSample{{SampleIndex: s.index, AvgNs: s.avgNs, InnerRSDPPM: s.innerRSDPPM, Batches: s.batches}},
		}
	}

	avgs := make([]int64, n)
	var minNs int64 = math.MaxInt64
	var maxNs int64 = 0
	var totalNs int64 = 0
	var totalIter int64 = 0

	for i, s := range sampleList {
		avgs[i] = s.avgNs
		if s.minNs < minNs {
			minNs = s.minNs
		}
		if s.maxNs > maxNs {
			maxNs = s.maxNs
		}
		totalNs += s.totalNs
		totalIter += s.iterations
	}

	avgNs := mean(avgs)
	variance := sampleVariance(avgs)
	stdDevNs := int64(math.Sqrt(*variance))
	p50Ns := percentile(avgs, 0.50)
	p95Ns := percentile(avgs, 0.95)
	p99Ns := percentile(avgs, 0.99)

	result := &db.Result{
		Category:             category,
		Name:                 name,
		MinNs:                minNs,
		AvgNs:                avgNs,
		MaxNs:                maxNs,
		StdDevNs:             stdDevNs,
		P50Ns:                p50Ns,
		P95Ns:                p95Ns,
		P99Ns:                p99Ns,
		TotalNs:              totalNs,
		Iterations:           totalIter,
		SampleCount:          int64(n),
		SampleAvgVarianceNs2: variance,
		SampleDataVersion:    db.CurrentSampleDataVersion,
		SummaryVersion:       db.CurrentSummaryVersion,
	}
	for _, s := range sampleList {
		result.Samples = append(result.Samples, db.ResultSample{SampleIndex: s.index, AvgNs: s.avgNs, InnerRSDPPM: s.innerRSDPPM, Batches: s.batches})
	}
	return result
}

func mean(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, v := range values {
		sum += v
	}
	return sum / int64(len(values))
}

func sampleVariance(values []int64) *float64 {
	n := len(values)
	if n < 2 {
		return nil
	}
	var mean, sumSquares float64
	for i, v := range values {
		delta := float64(v) - mean
		mean += delta / float64(i+1)
		sumSquares += delta * (float64(v) - mean)
	}
	variance := sumSquares / float64(n-1)
	return &variance
}

func percentile(values []int64, p float64) int64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return values[0]
	}

	sorted := make([]int64, n)
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := p * float64(n-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= n {
		return sorted[n-1]
	}

	frac := idx - float64(lower)
	return int64(float64(sorted[lower])*(1-frac) + float64(sorted[upper])*frac)
}
