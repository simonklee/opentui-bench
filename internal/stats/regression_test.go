package stats

import "testing"

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
