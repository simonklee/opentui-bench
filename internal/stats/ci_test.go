package stats

import (
	"math"
	"testing"
)

func expectedCI(avgNs, stdDevNs, sampleCount int64, tCrit float64) (int64, int64, int64) {
	semF := float64(stdDevNs) / math.Sqrt(float64(sampleCount))
	margin := tCrit * semF
	lowerF := float64(avgNs) - margin
	if lowerF < 0 {
		lowerF = 0
	}
	upperF := float64(avgNs) + margin
	return int64(math.Round(lowerF)), int64(math.Round(upperF)), int64(math.Round(semF))
}

func TestMeanCI95(t *testing.T) {
	t.Run("returns avg when sample count too small", func(t *testing.T) {
		lower, upper, sem := MeanCI95(100, 25, 1)
		if lower != 100 || upper != 100 || sem != 0 {
			t.Fatalf("expected avg bounds and sem=0, got lower=%d upper=%d sem=%d", lower, upper, sem)
		}
	})

	t.Run("returns avg when stddev is zero", func(t *testing.T) {
		lower, upper, sem := MeanCI95(100, 0, 10)
		if lower != 100 || upper != 100 || sem != 0 {
			t.Fatalf("expected avg bounds and sem=0, got lower=%d upper=%d sem=%d", lower, upper, sem)
		}
	})

	t.Run("uses t-critical for small samples", func(t *testing.T) {
		wantLower, wantUpper, wantSem := expectedCI(100, 20, 4, 3.182)
		lower, upper, sem := MeanCI95(100, 20, 4)
		if lower != wantLower || upper != wantUpper || sem != wantSem {
			t.Fatalf("expected lower=%d upper=%d sem=%d, got lower=%d upper=%d sem=%d", wantLower, wantUpper, wantSem, lower, upper, sem)
		}
	})

	t.Run("uses z-critical for large samples", func(t *testing.T) {
		wantLower, wantUpper, wantSem := expectedCI(1000, 100, 30, 1.96)
		lower, upper, sem := MeanCI95(1000, 100, 30)
		if lower != wantLower || upper != wantUpper || sem != wantSem {
			t.Fatalf("expected lower=%d upper=%d sem=%d, got lower=%d upper=%d sem=%d", wantLower, wantUpper, wantSem, lower, upper, sem)
		}
	})

	t.Run("clamps negative lower bound", func(t *testing.T) {
		wantLower, wantUpper, wantSem := expectedCI(10, 50, 2, 12.706)
		lower, upper, sem := MeanCI95(10, 50, 2)
		if lower != wantLower || upper != wantUpper || sem != wantSem {
			t.Fatalf("expected lower=%d upper=%d sem=%d, got lower=%d upper=%d sem=%d", wantLower, wantUpper, wantSem, lower, upper, sem)
		}
		if lower < 0 {
			t.Fatalf("expected lower bound to be clamped, got %d", lower)
		}
	})
}
