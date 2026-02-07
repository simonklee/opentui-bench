package stats

import "testing"

func TestEffectiveDegreesOfFreedom(t *testing.T) {
	t.Run("uses baseline point count when available", func(t *testing.T) {
		df := effectiveDegreesOfFreedom(100, 3, 10000, 20)
		if df != 19 {
			t.Fatalf("expected df=19, got %d", df)
		}
	})

	t.Run("falls back to latest df when baseline count missing", func(t *testing.T) {
		df := effectiveDegreesOfFreedom(100, 3, 10000, 0)
		if df != 2 {
			t.Fatalf("expected df=2 fallback, got %d", df)
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
