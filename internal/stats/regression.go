package stats

import (
	"errors"
	"math"
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
	RunID    int64   // ID of the run chosen as baseline reference
	Median   float64 // Baseline median (median of run medians)
	Variance float64 // Run-to-run variance of medians
	CILower  float64 // 95% CI lower bound
	CIUpper  float64 // 95% CI upper bound
	CV       float64 // Coefficient of variation (run-to-run noise)
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

// Errors returned by regression detection.
var (
	ErrInsufficientData = errors.New("insufficient data for regression analysis")
)

// tCriticalOneSided99 maps degrees of freedom to t-critical values for 99% one-sided test.
// Used for regression detection with alpha = 0.01.
var tCriticalOneSided99 = []float64{
	0,
	31.821, // df=1
	6.965,  // df=2
	4.541,  // df=3
	3.747,  // df=4
	3.365,  // df=5
	3.143,  // df=6
	2.998,  // df=7
	2.896,  // df=8
	2.821,  // df=9
	2.764,  // df=10
	2.718,  // df=11
	2.681,  // df=12
	2.650,  // df=13
	2.624,  // df=14
	2.602,  // df=15
	2.583,  // df=16
	2.567,  // df=17
	2.552,  // df=18
	2.539,  // df=19
	2.528,  // df=20
	2.518,  // df=21
	2.508,  // df=22
	2.500,  // df=23
	2.492,  // df=24
	2.485,  // df=25
	2.479,  // df=26
	2.473,  // df=27
	2.467,  // df=28
	2.462,  // df=29
	2.457,  // df=30
}

// TCriticalOneSided returns the t-critical value for a one-sided test at alpha level.
// Currently supports alpha = 0.01 (99% confidence).
func TCriticalOneSided(df int, alpha float64) float64 {
	if alpha != 0.01 {
		// For other alpha levels, use asymptotic z-value
		// This is a simplification; full implementation would use inverse-t
		return 2.326 // z for 99% one-sided
	}
	if df < 1 {
		return tCriticalOneSided99[1]
	}
	if df < len(tCriticalOneSided99) {
		return tCriticalOneSided99[df]
	}
	return 2.326 // asymptotic z for 99% one-sided
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
// - Variance: run-to-run variance of medians (used for t-test)
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

	// For small samples, use s2 directly as it's more conservative
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
		RunID:    baselineRunID,
		Median:   baselineMedian,
		Variance: combinedVar,
		CILower:  ciLower,
		CIUpper:  ciUpper,
		CV:       cv,
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

	// Degrees of freedom (Welch-Satterthwaite approximation simplified)
	// Use the latest sample count as a conservative proxy
	df := int(latest.SampleCount - 1)
	if df < 1 {
		df = 1
	}

	// One-sided t-critical value
	tCrit := TCriticalOneSided(df, alpha)

	// Effect size as percentage
	effectPct := 0.0
	if baseline.Median > 0 {
		effectPct = (diff / baseline.Median) * 100.0
	}

	// P-value approximation (one-sided)
	// This is a rough approximation; full implementation would use t-distribution CDF
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

// approximatePValue provides a rough p-value approximation for one-sided t-test.
// This is a simplified approximation; production code should use a proper t-distribution CDF.
func approximatePValue(t float64, df int) float64 {
	if t <= 0 {
		return 0.5 // Not slower than baseline
	}

	// Use normal approximation for larger df
	if df > 30 {
		// Approximate using standard normal
		// P(Z > t) for one-sided test
		return 0.5 * math.Erfc(t/math.Sqrt2)
	}

	// For small df, use a rough lookup-based approximation
	// This maps t-values to approximate p-values
	// Better implementations would use numerical integration or lookup tables
	if t > 4.0 {
		return 0.001
	}
	if t > 3.0 {
		return 0.005
	}
	if t > 2.5 {
		return 0.01
	}
	if t > 2.0 {
		return 0.025
	}
	if t > 1.5 {
		return 0.05
	}
	if t > 1.0 {
		return 0.15
	}
	return 0.3
}
