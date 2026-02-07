package stats

import (
	"math"
	"testing"
)

func TestRegressionDegreesOfFreedom(t *testing.T) {
	t.Run("uses baseline point count when available", func(t *testing.T) {
		df := regressionDegreesOfFreedom(3, 20)
		if df != 19 {
			t.Fatalf("expected df=19, got %d", df)
		}
	})

	t.Run("falls back to latest df when baseline count missing", func(t *testing.T) {
		df := regressionDegreesOfFreedom(3, 0)
		if df != 2 {
			t.Fatalf("expected df=2 fallback, got %d", df)
		}
	})

	t.Run("returns one when both counts are too small", func(t *testing.T) {
		df := regressionDegreesOfFreedom(1, 1)
		if df != 1 {
			t.Fatalf("expected df=1, got %d", df)
		}
	})
}

func TestTCriticalOneSided(t *testing.T) {
	testCases := []struct {
		name     string
		df       int
		alpha    float64
		expected float64
	}{
		{name: "df10_alpha005", df: 10, alpha: 0.05, expected: 1.812},
		{name: "df10_alpha001", df: 10, alpha: 0.01, expected: 2.764},
		{name: "df20_alpha005", df: 20, alpha: 0.05, expected: 1.725},
		{name: "df20_alpha001", df: 20, alpha: 0.01, expected: 2.528},
		{name: "df5_alpha0005", df: 5, alpha: 0.005, expected: 4.032},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := TCriticalOneSided(tc.df, tc.alpha)
			if math.Abs(got-tc.expected) > 0.01 {
				t.Fatalf("expected t-critical ~= %.3f, got %.6f", tc.expected, got)
			}
		})
	}
}

func TestTCriticalOneSidedEdgeCases(t *testing.T) {
	t.Run("alpha less than or equal zero returns +Inf", func(t *testing.T) {
		got := TCriticalOneSided(10, 0)
		if !math.IsInf(got, 1) {
			t.Fatalf("expected +Inf, got %v", got)
		}

		got = TCriticalOneSided(10, -0.1)
		if !math.IsInf(got, 1) {
			t.Fatalf("expected +Inf for negative alpha, got %v", got)
		}
	})

	t.Run("alpha greater than or equal 0.5 returns zero", func(t *testing.T) {
		for _, alpha := range []float64{0.5, 0.6, 1.0} {
			got := TCriticalOneSided(10, alpha)
			if got != 0 {
				t.Fatalf("alpha=%v: expected 0, got %v", alpha, got)
			}
		}
	})

	t.Run("df less than one clamps to one", func(t *testing.T) {
		clamped := TCriticalOneSided(0, 0.01)
		dfOne := TCriticalOneSided(1, 0.01)
		if math.Abs(clamped-dfOne) > 1e-9 {
			t.Fatalf("expected df<1 to match df=1, got df<1=%v df=1=%v", clamped, dfOne)
		}
	})
}

func TestDetectRegressionUsesBaselinePointCount(t *testing.T) {
	latest := RunStat{
		RunID:       2,
		Median:      400,
		Sem:         10,
		SampleCount: 3,
		StdDev:      20,
	}

	t.Run("regresses when baseline history provides meaningful df", func(t *testing.T) {
		baseline := &BaselineStats{
			RunID:      1,
			Median:     100,
			Variance:   10000,
			PointCount: 20,
			CILower:    90,
			CIUpper:    110,
			CV:         0,
		}

		result := DetectRegression(latest, baseline, 0.01)
		if result.Status != "regressed" {
			t.Fatalf("expected regressed status, got %q", result.Status)
		}
		if result.ChangePercent == nil {
			t.Fatal("expected change percent for regression")
		}
	})

	t.Run("stays ok when baseline point count is unavailable", func(t *testing.T) {
		baseline := &BaselineStats{
			RunID:      1,
			Median:     100,
			Variance:   10000,
			PointCount: 0,
			CILower:    90,
			CIUpper:    110,
			CV:         0,
		}

		result := DetectRegression(latest, baseline, 0.01)
		if result.Status != "ok" {
			t.Fatalf("expected ok status, got %q", result.Status)
		}
	})
}

func TestComputeBaselineCIWidth(t *testing.T) {
	buildHistory := func(n int) []RunStat {
		pattern := []float64{95, 98, 101, 99, 102, 97, 100, 103, 96, 104}
		history := make([]RunStat, n)
		for i := 0; i < n; i++ {
			history[i] = RunStat{
				RunID:       int64(n - i),
				Median:      pattern[i%len(pattern)],
				Sem:         0.5,
				SampleCount: 30,
				StdDev:      2,
			}
		}
		return history
	}

	baseline10, err := ComputeBaseline(buildHistory(10), 10, 0)
	if err != nil {
		t.Fatalf("ComputeBaseline(10) returned error: %v", err)
	}

	baseline20, err := ComputeBaseline(buildHistory(20), 10, 0)
	if err != nil {
		t.Fatalf("ComputeBaseline(20) returned error: %v", err)
	}

	if baseline20.Variance >= baseline10.Variance {
		t.Fatalf("expected n=20 baseline variance < n=10 baseline variance, got n20=%f n10=%f", baseline20.Variance, baseline10.Variance)
	}

	ratio := baseline20.Variance / baseline10.Variance
	if ratio < 0.4 || ratio > 0.6 {
		t.Fatalf("expected variance ratio near 0.5, got %f (n20=%f n10=%f)", ratio, baseline20.Variance, baseline10.Variance)
	}
}
