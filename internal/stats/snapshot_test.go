package stats

import (
	"testing"
	"time"
)

func snapshotObservation(id int64, at time.Time, avg float64) OrderedRunStat {
	return OrderedRunStat{
		RunDate: at,
		Stat:    RunStat{RunID: id, Avg: avg},
	}
}

func TestEvaluateSnapshotExcludesTargetAndFuture(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	target := snapshotObservation(4, at.Add(3*time.Hour), 200)
	observations := []OrderedRunStat{
		snapshotObservation(1, at, 100),
		target,
		snapshotObservation(2, at.Add(time.Hour), 100),
		snapshotObservation(5, at.Add(4*time.Hour), 10000),
		snapshotObservation(3, at.Add(2*time.Hour), 100),
	}

	evaluation := EvaluateSnapshot(target, observations, SnapshotConfig{Window: 3, MinPoints: 3})
	if evaluation.Baseline == nil {
		t.Fatal("expected baseline")
	}
	if !closeEnough(evaluation.Baseline.BaselineNs, 100) {
		t.Fatalf("baseline = %v, want 100", evaluation.Baseline.BaselineNs)
	}
	if len(evaluation.History) != 3 {
		t.Fatalf("history length = %d, want 3", len(evaluation.History))
	}
	for _, point := range evaluation.History {
		if point.RunID == target.Stat.RunID || point.RunID == 5 {
			t.Fatalf("non-prior run %d leaked into history", point.RunID)
		}
	}
}

func TestEvaluateSnapshotUsesInstantThenIDOrdering(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	target := snapshotObservation(4, at, 100)
	observations := []OrderedRunStat{
		snapshotObservation(5, at, 500),
		snapshotObservation(2, at, 200),
		snapshotObservation(3, at, 300),
		snapshotObservation(1, at.Add(-time.Hour), 100),
	}

	evaluation := EvaluateSnapshot(target, observations, SnapshotConfig{Window: 2, MinPoints: 2, BaselineOffset: 1})
	if len(evaluation.History) != 2 {
		t.Fatalf("history length = %d, want 2", len(evaluation.History))
	}
	if evaluation.History[0].RunID != 2 || evaluation.History[1].RunID != 1 {
		t.Fatalf("history IDs = [%d %d], want [2 1]", evaluation.History[0].RunID, evaluation.History[1].RunID)
	}
}
