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
	CommitHash     string
	CommitHashFull string
	CommitMessage  string
	CommitDate     string
	Branch         string
	MachineID      string
	Notes          string
	ZigOptimize    string
	SampleCount    int
}

// ParsedRun contains a fully parsed and aggregated benchmark run, ready for
// storage (local DB or remote API). Built entirely in memory with no side effects.
type ParsedRun struct {
	Meta    RunMetadata
	Results []ParsedResult
}

// ParsedResult is a single aggregated benchmark result with optional memory stats.
type ParsedResult struct {
	Category    string
	Name        string
	MinNs       int64
	AvgNs       int64
	MaxNs       int64
	StdDevNs    int64
	P50Ns       int64
	P95Ns       int64
	P99Ns       int64
	TotalNs     int64
	Iterations  int64
	SampleCount int64
	MemStats    []MemStatJSON
}

type sample struct {
	minNs      int64
	avgNs      int64
	maxNs      int64
	totalNs    int64
	iterations int64
	memStats   []MemStatJSON
}

type benchmarkKey struct {
	category string
	name     string
}

// Parse reads benchmark JSON output and aggregates samples into a ParsedRun.
// It does everything Record() does up to the DB write: parses JSON, aggregates
// samples, builds the result structs. No DB dependency. No side effects.
func Parse(reader io.Reader, meta RunMetadata) (*ParsedRun, error) {
	if meta.ZigOptimize == "" {
		meta.ZigOptimize = "ReleaseFast"
	}

	samples := make(map[benchmarkKey][]sample)
	keyOrder := []benchmarkKey{}

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
			return nil, fmt.Errorf("parse benchmark JSON on line %d: %w", lineNum, err)
		}

		for _, r := range bench.Results {
			key := benchmarkKey{category: bench.Benchmark, name: r.Name}
			if _, exists := samples[key]; !exists {
				keyOrder = append(keyOrder, key)
			}
			samples[key] = append(samples[key], sample{
				minNs:      r.MinNs,
				avgNs:      r.AvgNs,
				maxNs:      r.MaxNs,
				totalNs:    r.TotalNs,
				iterations: r.Iterations,
				memStats:   r.MemStats,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan input: %w", err)
	}

	parsed := &ParsedRun{Meta: meta}

	for _, key := range keyOrder {
		sampleList := samples[key]
		agg := aggregateSamples(key.category, key.name, sampleList)

		var memStats []MemStatJSON
		if len(sampleList) > 0 && len(sampleList[0].memStats) > 0 {
			memStats = sampleList[0].memStats
		}

		parsed.Results = append(parsed.Results, ParsedResult{
			Category:    agg.Category,
			Name:        agg.Name,
			MinNs:       agg.MinNs,
			AvgNs:       agg.AvgNs,
			MaxNs:       agg.MaxNs,
			StdDevNs:    agg.StdDevNs,
			P50Ns:       agg.P50Ns,
			P95Ns:       agg.P95Ns,
			P99Ns:       agg.P99Ns,
			TotalNs:     agg.TotalNs,
			Iterations:  agg.Iterations,
			SampleCount: agg.SampleCount,
			MemStats:    memStats,
		})
	}

	return parsed, nil
}

// Store writes a ParsedRun to the database. Returns the run ID and result count.
func Store(database *db.DB, parsed *ParsedRun) (int64, int, error) {
	run := &db.Run{
		CommitHash:     parsed.Meta.CommitHash,
		CommitHashFull: parsed.Meta.CommitHashFull,
		CommitMessage:  parsed.Meta.CommitMessage,
		CommitDate:     parsed.Meta.CommitDate,
		Branch:         parsed.Meta.Branch,
		RunDate:        time.Now().Format(time.RFC3339),
		MachineID:      parsed.Meta.MachineID,
		Notes:          parsed.Meta.Notes,
		ZigOptimize:    parsed.Meta.ZigOptimize,
	}

	if run.ZigOptimize == "" {
		run.ZigOptimize = "ReleaseFast"
	}

	runID, err := database.InsertRun(run)
	if err != nil {
		return 0, 0, fmt.Errorf("insert run: %w", err)
	}
	cleanup := func() {
		_ = database.DeleteRun(runID)
	}

	for _, pr := range parsed.Results {
		result := &db.Result{
			RunID:       runID,
			Category:    pr.Category,
			Name:        pr.Name,
			MinNs:       pr.MinNs,
			AvgNs:       pr.AvgNs,
			MaxNs:       pr.MaxNs,
			StdDevNs:    pr.StdDevNs,
			P50Ns:       pr.P50Ns,
			P95Ns:       pr.P95Ns,
			P99Ns:       pr.P99Ns,
			TotalNs:     pr.TotalNs,
			Iterations:  pr.Iterations,
			SampleCount: pr.SampleCount,
		}

		resultID, err := database.InsertResult(result)
		if err != nil {
			cleanup()
			return 0, 0, fmt.Errorf("insert result: %w", err)
		}

		for _, ms := range pr.MemStats {
			stat := &db.MemStat{
				ResultID: resultID,
				StatName: ms.Name,
				Bytes:    ms.Bytes,
			}
			if err := database.InsertMemStat(stat); err != nil {
				cleanup()
				return 0, 0, fmt.Errorf("insert mem stat: %w", err)
			}
		}
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

	if n == 1 {
		s := sampleList[0]
		return &db.Result{
			Category:    category,
			Name:        name,
			MinNs:       s.minNs,
			AvgNs:       s.avgNs,
			MaxNs:       s.maxNs,
			StdDevNs:    0,
			P50Ns:       s.avgNs,
			P95Ns:       s.avgNs,
			P99Ns:       s.avgNs,
			TotalNs:     s.totalNs,
			Iterations:  s.iterations,
			SampleCount: 1,
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
	stdDevNs := stddev(avgs)
	p50Ns := percentile(avgs, 0.50)
	p95Ns := percentile(avgs, 0.95)
	p99Ns := percentile(avgs, 0.99)

	return &db.Result{
		Category:    category,
		Name:        name,
		MinNs:       minNs,
		AvgNs:       avgNs,
		MaxNs:       maxNs,
		StdDevNs:    stdDevNs,
		P50Ns:       p50Ns,
		P95Ns:       p95Ns,
		P99Ns:       p99Ns,
		TotalNs:     totalNs,
		Iterations:  totalIter,
		SampleCount: int64(n),
	}
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

func stddev(values []int64) int64 {
	n := len(values)
	if n < 2 {
		return 0
	}

	avg := mean(values)
	var sumSquares float64
	for _, v := range values {
		diff := float64(v - avg)
		sumSquares += diff * diff
	}
	variance := sumSquares / float64(n-1)
	return int64(math.Sqrt(variance))
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
