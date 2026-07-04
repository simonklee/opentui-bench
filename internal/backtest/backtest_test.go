package backtest

import (
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/stats"
)

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
		{Scorecard: Scorecard{TotalAlerts: 20}},
		{Scorecard: Scorecard{TotalAlerts: 10}},
		{Scorecard: Scorecard{TotalAlerts: 2}},
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

	if results[0].Scorecard.RetainedAlertSignal != 1 {
		t.Fatalf("expected first retained signal to be 1, got %.3f", results[0].Scorecard.RetainedAlertSignal)
	}
}

func TestBroadShiftMetricsDoNotAffectRecommendationScore(t *testing.T) {
	results := []ConfigResult{
		{Scorecard: Scorecard{AlertsPerRun: 1, TotalAlerts: 10, CategoryHHI: 0.2}},
		{Scorecard: Scorecard{
			AlertsPerRun: 1, TotalAlerts: 10, CategoryHHI: 0.2,
			NearShiftAlertFraction: 1, BurstinessAfterShift: 100, PersistenceAfterShift: 100,
		}},
	}
	assignObjectiveScores(results)
	applyRetentionConstraint(results, 0.25)
	if results[0].ObjectiveScore != results[1].ObjectiveScore ||
		results[0].EligibleForRecommendation != results[1].EligibleForRecommendation {
		t.Fatalf("broad incident context changed recommendation: %+v", results)
	}
}

func TestBroadShiftIsScorecardContextAndDoesNotSuppressAlert(t *testing.T) {
	opts := DefaultOptions()
	opts.Window = 5
	opts.MinPoints = 5
	opts.BaselineOffset = 0
	opts.BroadShiftMinBench = 1

	base := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	observations := make([]stats.OrderedRunStat, 0, 6)
	for i := 0; i < 5; i++ {
		observations = append(observations, stats.OrderedRunStat{
			RunDate: base.Add(time.Duration(i) * time.Hour),
			Stat:    stats.RunStat{RunID: int64(i + 1), Avg: 100_000 + float64(i*100)},
		})
	}
	target := stats.OrderedRunStat{RunDate: base.Add(5 * time.Hour), Stat: stats.RunStat{RunID: 6, Avg: 130_000}}
	observations = append(observations, target)
	ctx := runContext{
		RunID:                6,
		AnalyzableBenchmarks: 1,
		Benchmarks: []benchmarkContext{{
			Category: "render", Target: target, Observations: observations,
		}},
	}

	results := map[int64][]db.Result{
		5: {{Category: "render", Name: "frame", AvgNs: 100_400}},
		6: {{Category: "render", Name: "frame", AvgNs: 130_000}},
	}
	incident, err := computeBroadShift(func(runID int64) ([]db.Result, error) { return results[runID], nil }, 6, 5, opts)
	if err != nil || !incident.Detected {
		t.Fatalf("broad shift = %+v, err=%v", incident, err)
	}
	outcome := evaluateConfigForRun(ctx, Config{}, opts)
	if outcome.AlertCount != 1 {
		t.Fatalf("alert count = %d, broad-shift context must not suppress it", outcome.AlertCount)
	}
}
