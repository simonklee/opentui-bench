package stats

import (
	"errors"
	"math"
	"sort"
)

// RunStat represents the statistical summary of a single benchmark run.
// Median is the primary metric for regression detection (robust to outliers).
type RunStat struct {
	RunID       int64
	Median      float64 // Primary metric: p50 of sample measurements
	Sem         float64 // Standard error (based on sample variance)
	SampleCount int64
	StdDev      float64
}

// BaselineStats represents the computed baseline from historical runs.
// Uses median-based statistics for robustness against outliers.
type BaselineStats struct {
	RunID      int64   // ID of the run chosen as baseline reference
	Median     float64 // Baseline median (median of run medians)
	Variance   float64 // Variance used for baseline-estimator uncertainty in DetectRegression
	PointCount int     // Number of valid historical runs used for baseline stats
	CILower    float64 // 95% CI lower bound
	CIUpper    float64 // 95% CI upper bound
	CV         float64 // Coefficient of variation (run-to-run noise)
}

// RegressionResult represents the outcome of regression detection for a single point.
type RegressionResult struct {
	Status           string   // "ok", "regressed", "baseline", "insufficient"
	BaselineRunID    *int64   // nil if insufficient data
	BaselineCILower  *float64 // nil if insufficient data
	BaselineCIUpper  *float64 // nil if insufficient data
	ChangePercent    *float64 // nil if not regressed
	MinEffectPercent float64  // Dynamic threshold based on CV
	PValue           *float64 // nil if not computed
}

// BHResult holds a benchmark's regression result after FDR correction.
type BHResult struct {
	Index         int
	PValue        float64
	AdjPValue     float64
	IsSignificant bool
}

// BenjaminiHochberg applies Benjamini-Hochberg FDR correction to a set of p-values.
// Returns adjusted p-values and significance flags at the given FDR level.
func BenjaminiHochberg(pValues []float64, fdr float64) []BHResult {
	m := len(pValues)
	if m == 0 {
		return nil
	}

	type rankedPValue struct {
		Index     int
		PValue    float64
		AdjPValue float64
	}

	ranked := make([]rankedPValue, m)
	for i, p := range pValues {
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		ranked[i] = rankedPValue{Index: i, PValue: p}
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].PValue < ranked[j].PValue
	})

	for i := range ranked {
		rank := float64(i + 1)
		ranked[i].AdjPValue = ranked[i].PValue * float64(m) / rank
	}

	runningMin := 1.0
	for i := m - 1; i >= 0; i-- {
		if ranked[i].AdjPValue < runningMin {
			runningMin = ranked[i].AdjPValue
		}
		if runningMin > 1 {
			runningMin = 1
		}
		ranked[i].AdjPValue = runningMin
	}

	results := make([]BHResult, m)
	for _, rp := range ranked {
		results[rp.Index] = BHResult{
			Index:         rp.Index,
			PValue:        rp.PValue,
			AdjPValue:     rp.AdjPValue,
			IsSignificant: rp.AdjPValue <= fdr,
		}
	}

	return results
}

// Errors returned by regression detection.
var (
	ErrInsufficientData = errors.New("insufficient data for regression analysis")
)

// TCriticalOneSided returns the t-critical value for a one-sided test.
// Solves for t where P(T > t) = alpha using bisection on tDistSurvival.
func TCriticalOneSided(df int, alpha float64) float64 {
	if df < 1 {
		df = 1
	}
	if alpha <= 0 {
		return math.Inf(1)
	}
	if alpha >= 0.5 {
		return 0
	}

	lo, hi := 0.0, 1.0
	for tDistSurvival(hi, df) > alpha && hi < 1e6 {
		hi *= 2
	}

	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if tDistSurvival(mid, df) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}

	return (lo + hi) / 2
}

// ComputeBaseline computes a stable baseline from historical runs using median-based statistics.
// Medians are inherently robust to outliers (GC pauses, OS scheduling), making this approach
// more reliable than mean-based methods for benchmark comparison.
//
// Returns nil if there are fewer than minPoints valid runs.
//
// baselineOffset skips the most recent N runs in history. history must be ordered
// newest-first when baselineOffset > 0.
//
// The returned BaselineStats contains:
// - Median: median of run medians (doubly robust to outliers)
// - Variance: baseline-estimator variance used for baseline uncertainty in DetectRegression
// - CILower/CIUpper: 95% CI around the baseline median
// - RunID: ID of the selected baseline reference run
// - CV: coefficient of variation for sensitivity tuning
func ComputeBaseline(history []RunStat, minPoints int, baselineOffset int) (*BaselineStats, error) {
	if baselineOffset < 0 {
		baselineOffset = 0
	}
	if baselineOffset > 0 && !isOrderedNewestFirst(history) {
		return nil, ErrInsufficientData
	}
	if baselineOffset >= len(history) {
		return nil, ErrInsufficientData
	}
	if baselineOffset > 0 {
		history = history[baselineOffset:]
	}
	if len(history) < minPoints {
		return nil, ErrInsufficientData
	}

	// Filter out invalid runs (need sample_count >= 2 and stdDev > 0)
	var valid []RunStat
	for _, s := range history {
		if s.SampleCount >= 2 && s.StdDev > 0 && s.Sem > 0 {
			valid = append(valid, s)
		}
	}

	if len(valid) < minPoints {
		return nil, ErrInsufficientData
	}

	// Collect medians from all valid runs
	medians := make([]float64, len(valid))
	sem2s := make([]float64, len(valid))
	for i, s := range valid {
		medians[i] = s.Median
		sem2s[i] = s.Sem * s.Sem
	}

	// Compute median of medians (doubly robust to outliers)
	baselineMedian := medianOfSlice(medians)

	// Run-to-run variance of medians
	meanOfMedians := mean(medians)
	s2 := variance(medians, meanOfMedians)

	// Mean of squared SEMs (within-run variance estimate)
	meanSem2 := mean(sem2s)

	// Combined variance estimate (similar to random-effects model)
	// tau^2 represents between-run variance
	tau2 := math.Max(0, s2-meanSem2)
	combinedVar := meanSem2 + tau2
	combinedVar = combinedVar / float64(len(valid))

	// For small samples, use s2/n directly as it's more conservative
	if len(valid) < 10 {
		combinedVar = s2 / float64(len(valid))
	}

	// Coefficient of variation (CV) based on run-to-run variance
	cv := 0.0
	if meanOfMedians > 0 {
		cv = math.Sqrt(s2) / meanOfMedians
	}

	// Compute 95% CI around the baseline median
	se := math.Sqrt(combinedVar)
	df := len(valid) - 1
	tCrit := 1.96 // default z-value for large samples
	if df > 0 && df < len(tCritical95) {
		tCrit = tCritical95[df]
	}
	ciLower := baselineMedian - tCrit*se
	ciUpper := baselineMedian + tCrit*se

	// Select a stable baseline run as reference (for identifying introducing runs)
	// Pick the run whose median is closest to the baseline median
	var baselineRunID int64
	minDist := math.MaxFloat64
	for _, s := range valid {
		dist := math.Abs(s.Median - baselineMedian)
		if dist < minDist {
			minDist = dist
			baselineRunID = s.RunID
		}
	}

	return &BaselineStats{
		RunID:      baselineRunID,
		Median:     baselineMedian,
		Variance:   combinedVar,
		PointCount: len(valid),
		CILower:    ciLower,
		CIUpper:    ciUpper,
		CV:         cv,
	}, nil
}

// DetectRegression tests if the latest run is statistically slower than the baseline.
// Uses median-based comparison with a one-sided t-test and a variance-tuned effect size gate.
// Medians are robust to outliers from GC pauses and OS scheduling.
func DetectRegression(latest RunStat, baseline *BaselineStats, alpha float64) RegressionResult {
	// Check if latest has valid data
	if latest.SampleCount < 2 || latest.StdDev <= 0 {
		return RegressionResult{
			Status: "insufficient",
		}
	}

	if baseline == nil {
		return RegressionResult{
			Status: "insufficient",
		}
	}

	// Variance-tuned minimum effect threshold
	// Noisy benchmarks need larger effect to flag; stable ones can detect smaller changes
	minEffectPct := math.Max(1.0, 2.0*baseline.CV*100.0)

	// Compute the difference using medians
	diff := latest.Median - baseline.Median

	// Standard error of the difference
	// Combines latest SEM with baseline variance
	seDiff := math.Sqrt(latest.Sem*latest.Sem + baseline.Variance)

	if seDiff == 0 {
		return RegressionResult{
			Status:           "ok",
			BaselineRunID:    &baseline.RunID,
			BaselineCILower:  &baseline.CILower,
			BaselineCIUpper:  &baseline.CIUpper,
			MinEffectPercent: minEffectPct,
		}
	}

	// t-statistic
	t := diff / seDiff

	// Degrees of freedom for regression significance.
	// We anchor to baseline history size when available because latest runs often
	// have very small sample counts (for example n=3), making latest-only df
	// unrealistically strict for run-over-run detection.
	df := regressionDegreesOfFreedom(int(latest.SampleCount), baseline.PointCount)

	// One-sided t-critical value
	tCrit := TCriticalOneSided(df, alpha)

	// Effect size as percentage
	effectPct := 0.0
	if baseline.Median > 0 {
		effectPct = (diff / baseline.Median) * 100.0
	}

	// One-sided p-value from the t-distribution.
	pValue := approximatePValue(t, df)

	// Is it a regression?
	// Must be both statistically significant AND practically significant
	isRegression := t > tCrit && effectPct >= minEffectPct

	result := RegressionResult{
		BaselineRunID:    &baseline.RunID,
		BaselineCILower:  &baseline.CILower,
		BaselineCIUpper:  &baseline.CIUpper,
		MinEffectPercent: minEffectPct,
		PValue:           &pValue,
	}

	if isRegression {
		result.Status = "regressed"
		result.ChangePercent = &effectPct
	} else {
		result.Status = "ok"
	}

	return result
}

// FindIntroducingRun walks through history to find the first run where regression was introduced.
// History should be in chronological order (oldest first).
// Returns nil if no introducing run is found.
func FindIntroducingRun(history []RunStat, baseline *BaselineStats, alpha float64) *int64 {
	if baseline == nil || len(history) == 0 {
		return nil
	}

	for _, run := range history {
		result := DetectRegression(run, baseline, alpha)
		if result.Status == "regressed" {
			id := run.RunID
			return &id
		}
	}

	return nil
}

// Helper functions

func isOrderedNewestFirst(history []RunStat) bool {
	if len(history) < 2 {
		return true
	}
	for i := 1; i < len(history); i++ {
		if history[i].RunID > history[i-1].RunID {
			return false
		}
	}
	return true
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func variance(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	sumSq := 0.0
	for _, v := range values {
		d := v - mean
		sumSq += d * d
	}
	return sumSq / float64(len(values)-1)
}

// regressionDegreesOfFreedom returns the degrees of freedom used for
// significance testing. Baseline history count is preferred when available,
// with latest-run sample count as fallback.
func regressionDegreesOfFreedom(latestCount int, baselineCount int) int {
	if baselineCount > 1 {
		return baselineCount - 1
	}
	if latestCount > 1 {
		return latestCount - 1
	}
	return 1
}

// medianOfSlice computes the median of a slice of float64 values.
// Does not modify the input slice.
func medianOfSlice(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return values[0]
	}

	// Make a copy to avoid modifying the input
	sorted := make([]float64, n)
	copy(sorted, values)
	sortFloat64s(sorted)

	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// sortFloat64s sorts a slice of float64 in ascending order (simple insertion sort for small slices).
func sortFloat64s(values []float64) {
	for i := 1; i < len(values); i++ {
		key := values[i]
		j := i - 1
		for j >= 0 && values[j] > key {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = key
	}
}

// approximatePValue returns the one-sided p-value for a t-statistic.
func approximatePValue(t float64, df int) float64 {
	return tDistSurvival(t, df)
}
