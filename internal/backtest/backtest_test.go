package backtest

import "testing"

func TestBuildConfigGrid(t *testing.T) {
	configs, err := BuildConfigGrid(
		[]float64{0, 1000},
	)
	if err != nil {
		t.Fatalf("BuildConfigGrid failed: %v", err)
	}

	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}
}

func TestBestResultIndex(t *testing.T) {
	results := []ConfigResult{
		{
			Config: Config{MinAbsoluteNs: 0},
			Scorecard: Scorecard{
				AlertsPerRun:           2.0,
				BurstinessAfterShift:   8,
				PersistenceAfterShift:  4,
				CategoryHHI:            0.40,
				NearShiftAlertFraction: 0.80,
				OffShiftAlertsPerRun:   0.20,
			},
		},
		{
			Config: Config{MinAbsoluteNs: 1000},
			Scorecard: Scorecard{
				AlertsPerRun:           1.0,
				BurstinessAfterShift:   2,
				PersistenceAfterShift:  1,
				CategoryHHI:            0.22,
				NearShiftAlertFraction: 0.30,
				OffShiftAlertsPerRun:   0.60,
			},
		},
	}

	assignObjectiveScores(results)
	applyRetentionConstraint(results, 0.25)
	best := bestResultIndex(results)
	if best != 1 {
		t.Fatalf("expected best index 1, got %d", best)
	}
	if results[1].ObjectiveScore >= results[0].ObjectiveScore {
		t.Fatalf("expected result[1] score < result[0] score (got %.4f >= %.4f)", results[1].ObjectiveScore, results[0].ObjectiveScore)
	}
}

func TestApplyRetentionConstraint(t *testing.T) {
	results := []ConfigResult{
		{Scorecard: Scorecard{OffShiftAlertsPerRun: 2.0}},
		{Scorecard: Scorecard{OffShiftAlertsPerRun: 1.0}},
		{Scorecard: Scorecard{OffShiftAlertsPerRun: 0.2}},
	}

	applyRetentionConstraint(results, 0.25)

	if !results[0].EligibleForRecommendation {
		t.Fatalf("expected first config to be eligible")
	}
	if !results[1].EligibleForRecommendation {
		t.Fatalf("expected second config to be eligible")
	}
	if results[2].EligibleForRecommendation {
		t.Fatalf("expected third config to be ineligible")
	}

	if results[0].Scorecard.RetainedOffShiftSignal != 1 {
		t.Fatalf("expected first retained signal to be 1, got %.3f", results[0].Scorecard.RetainedOffShiftSignal)
	}
}
