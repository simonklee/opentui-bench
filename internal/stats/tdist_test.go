package stats

import (
	"math"
	"testing"
)

func TestTDistSurvivalKnownValues(t *testing.T) {
	testCases := []struct {
		name     string
		df       int
		tValue   float64
		expected float64
	}{
		{name: "df1_t631", df: 1, tValue: 6.31, expected: 0.05},
		{name: "df5_t257", df: 5, tValue: 2.57, expected: 0.025},
		{name: "df10_t276", df: 10, tValue: 2.76, expected: 0.01},
		{name: "df30_t246", df: 30, tValue: 2.46, expected: 0.01},
		{name: "df100_t196", df: 100, tValue: 1.96, expected: 0.026},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tDistSurvival(tc.tValue, tc.df)
			if math.Abs(got-tc.expected) > 0.001 {
				t.Fatalf("expected p ~= %.6f, got %.6f", tc.expected, got)
			}
		})
	}
}

func TestTDistSurvivalAtZero(t *testing.T) {
	for _, df := range []int{1, 2, 5, 10, 30, 100} {
		got := tDistSurvival(0, df)
		if got != 0.5 {
			t.Fatalf("df=%d: expected 0.5 at t=0, got %.12f", df, got)
		}
	}
}

func TestTDistSurvivalSymmetry(t *testing.T) {
	for _, df := range []int{1, 5, 10, 30, 100} {
		pos := tDistSurvival(1.75, df)
		neg := tDistSurvival(-1.75, df)
		if math.Abs(neg-(1-pos)) > 1e-12 {
			t.Fatalf("df=%d: expected symmetry, got S(-t)=%.12f and 1-S(t)=%.12f", df, neg, 1-pos)
		}
	}
}

func TestTDistSurvivalMonotonicity(t *testing.T) {
	for _, df := range []int{1, 5, 10, 30, 100} {
		tValues := []float64{0, 0.5, 1.0, 1.5, 2.0, 2.5, 3.0}
		prev := tDistSurvival(tValues[0], df)
		for i := 1; i < len(tValues); i++ {
			curr := tDistSurvival(tValues[i], df)
			if curr > prev+1e-12 {
				t.Fatalf("df=%d: expected monotone decrease, got S(%.2f)=%.12f > S(%.2f)=%.12f", df, tValues[i], curr, tValues[i-1], prev)
			}
			prev = curr
		}
	}
}
