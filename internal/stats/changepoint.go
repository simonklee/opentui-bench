package stats

import (
	"math"
	"math/rand"
	"sort"
)

// ChangePoint represents a detected shift in the time series.
type ChangePoint struct {
	Index         int
	RunID         int64
	Magnitude     float64
	EffectPercent float64
	PValue        float64
}

// DetectChangePoints finds statistically significant distribution shifts
// in a time series of run averages using an E-Divisive-style recursion.
//
// series must be in chronological order (oldest first).
func DetectChangePoints(series []RunStat, minSegment int, alpha float64, nPerms int) []ChangePoint {
	if minSegment < 1 {
		minSegment = 1
	}
	if alpha <= 0 {
		alpha = 0.05
	}
	if alpha >= 1 {
		alpha = 0.999999
	}
	if nPerms < 1 {
		nPerms = 1
	}
	if len(series) < 2*minSegment {
		return nil
	}

	values := make([]float64, len(series))
	for i, s := range series {
		values[i] = s.Avg
	}

	rng := rand.New(rand.NewSource(42))
	var points []ChangePoint
	eDivisive(values, series, 0, len(values), minSegment, alpha, nPerms, rng, &points)

	if len(points) == 0 {
		return nil
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Index < points[j].Index
	})

	return points
}

func eDivisive(values []float64, series []RunStat, start, end, minSeg int, alpha float64, nPerms int, rng *rand.Rand, out *[]ChangePoint) {
	n := end - start
	if n < 2*minSeg {
		return
	}

	relK, observedE := findBestSplit(values[start:end], minSeg)
	if relK < minSeg || relK > n-minSeg || observedE <= 0 {
		return
	}

	pValue := permutationPValue(values[start:end], observedE, minSeg, nPerms, rng)
	if pValue > alpha {
		return
	}

	absK := start + relK
	beforeMedian := medianOfSlice(values[start:absK])
	afterMedian := medianOfSlice(values[absK:end])
	effectPct := 0.0
	if beforeMedian != 0 {
		effectPct = (afterMedian - beforeMedian) / beforeMedian * 100.0
	}

	*out = append(*out, ChangePoint{
		Index:         absK,
		RunID:         series[absK].RunID,
		Magnitude:     afterMedian - beforeMedian,
		EffectPercent: effectPct,
		PValue:        pValue,
	})

	eDivisive(values, series, start, absK, minSeg, alpha, nPerms, rng, out)
	eDivisive(values, series, absK, end, minSeg, alpha, nPerms, rng, out)
}

func findBestSplit(values []float64, minSeg int) (int, float64) {
	n := len(values)
	if n < 2*minSeg {
		return -1, 0
	}

	bestK := -1
	bestE := math.Inf(-1)
	for k := minSeg; k <= n-minSeg; k++ {
		e := energyStatistic(values, k)
		if e > bestE {
			bestE = e
			bestK = k
		}
	}

	if bestK == -1 {
		return -1, 0
	}
	return bestK, bestE
}

func energyStatistic(values []float64, k int) float64 {
	n := len(values)
	if k <= 0 || k >= n {
		return 0
	}

	n1 := float64(k)
	n2 := float64(n - k)

	var cross, withinLeft, withinRight float64

	for i := 0; i < k; i++ {
		for j := 0; j < k; j++ {
			withinLeft += math.Abs(values[i] - values[j])
		}
		for j := k; j < n; j++ {
			cross += math.Abs(values[i] - values[j])
		}
	}

	for i := k; i < n; i++ {
		for j := k; j < n; j++ {
			withinRight += math.Abs(values[i] - values[j])
		}
	}

	return (2.0/(n1*n2))*cross - (1.0/(n1*n1))*withinLeft - (1.0/(n2*n2))*withinRight
}

func permutationPValue(values []float64, observedE float64, minSeg int, nPerms int, rng *rand.Rand) float64 {
	if observedE <= 0 {
		return 1
	}

	perm := make([]float64, len(values))
	count := 0
	for p := 0; p < nPerms; p++ {
		copy(perm, values)
		rng.Shuffle(len(perm), func(i, j int) {
			perm[i], perm[j] = perm[j], perm[i]
		})
		_, maxE := findBestSplit(perm, minSeg)
		if maxE >= observedE {
			count++
		}
	}

	return float64(count+1) / float64(nPerms+1)
}
