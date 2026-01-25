package stats

import "math"

// tCritical95 maps degrees of freedom to t-critical values for 95% CI.
var tCritical95 = []float64{
	0,
	12.706,
	4.303,
	3.182,
	2.776,
	2.571,
	2.447,
	2.365,
	2.306,
	2.262,
	2.228,
	2.201,
	2.179,
	2.160,
	2.145,
	2.131,
	2.120,
	2.110,
	2.101,
	2.093,
	2.086,
	2.080,
	2.074,
	2.069,
	2.064,
	2.060,
	2.056,
	2.052,
	2.048,
	2.045,
	2.042,
}

// CI95 computes a 95% confidence interval around the given value.
// Uses standard error computed from sample standard deviation.
// This works for both mean and median when the sample size is sufficient.
//
// For rigorous median CI, bootstrap methods would be more appropriate,
// but the parametric approximation is acceptable for benchmark comparison
// with typical sample sizes (3-30 samples).
func CI95(valueNs, stdDevNs, sampleCount int64) (lower, upper, sem int64) {
	if sampleCount < 2 || stdDevNs == 0 {
		return valueNs, valueNs, 0
	}

	semF := float64(stdDevNs) / math.Sqrt(float64(sampleCount))
	tCrit := 1.96
	if sampleCount < 30 {
		df := int(sampleCount - 1)
		if df > 0 && df < len(tCritical95) {
			tCrit = tCritical95[df]
		}
	}

	margin := tCrit * semF
	lowerF := float64(valueNs) - margin
	if lowerF < 0 {
		lowerF = 0
	}
	upperF := float64(valueNs) + margin

	return int64(math.Round(lowerF)), int64(math.Round(upperF)), int64(math.Round(semF))
}
