package calibration

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/stats"
)

func fixtureRun(id int64, at time.Time, commit string, avg, median int64) runData {
	return runData{
		Run:     db.Run{ID: id, CommitHash: commit, CommitHashFull: commit, Branch: "main", RunDate: at.Format(time.RFC3339), MachineID: "host", ZigOptimize: "ReleaseFast"},
		At:      at,
		Results: []db.Result{{ID: id, RunID: id, Category: "render", Name: "frame", AvgNs: avg, P50Ns: median, StdDevNs: 100, SampleCount: 3}},
	}
}

func findSnapshot(t *testing.T, snapshots []snapshot, runID int64) snapshot {
	t.Helper()
	for _, snapshot := range snapshots {
		if snapshot.Run.ID == runID {
			return snapshot
		}
	}
	t.Fatalf("snapshot %d not found", runID)
	return snapshot{}
}

func TestReplayIsChronologicalAndFutureProof(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var runs []runData
	for i := 1; i <= 10; i++ {
		runs = append(runs, fixtureRun(int64(i), base.Add(time.Duration(i)*time.Hour), "c", 100_000+int64(i), 100_000+int64(i)))
	}
	runs = append(runs, fixtureRun(11, base.Add(11*time.Hour), "target", 120_000, 120_000))
	before, _, err := replay(runs, "main")
	if err != nil {
		t.Fatal(err)
	}
	runs = append([]runData{fixtureRun(12, base.Add(12*time.Hour), "future", 9_000_000, 9_000_000)}, runs...)
	after, _, err := replay(runs, "main")
	if err != nil {
		t.Fatal(err)
	}
	a := findSnapshot(t, before, 11)
	b := findSnapshot(t, after, 11)
	if len(a.Hypotheses) != 1 || len(b.Hypotheses) != 1 || a.Hypotheses[0].Evaluation.Baseline.LogMean != b.Hypotheses[0].Evaluation.Baseline.LogMean {
		t.Fatalf("future run changed target snapshot: before=%+v after=%+v", a, b)
	}
	for _, history := range b.Hypotheses[0].Evaluation.History {
		if history.RunID >= 11 {
			t.Fatalf("non-prior run %d leaked into target history", history.RunID)
		}
	}
}

func TestRepeatedCommitGroupingUsesExactCohort(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var runs []runData
	for i := 1; i <= 10; i++ {
		commit := "commit-" + string(rune('a'+i))
		if i == 10 {
			commit = "commit-b"
		}
		runs = append(runs, fixtureRun(int64(i), base.Add(time.Duration(i)*time.Hour), commit, 100_000+int64(i), 100_000+int64(i)))
	}
	snapshots, _, err := replay(runs, "main")
	if err != nil {
		t.Fatal(err)
	}
	repeated := findSnapshot(t, snapshots, 10)
	if !repeated.RepeatedNull || len(repeated.Hypotheses) != 1 {
		t.Fatalf("repeated target not grouped: %+v", repeated)
	}
	runs[0].Run.MachineID = "other-host"
	snapshots, _, _ = replay(runs, "main")
	if findSnapshot(t, snapshots, 10).RepeatedNull {
		t.Fatal("commit from a different cohort was grouped as unchanged")
	}
}

func TestLag1Autocorrelation(t *testing.T) {
	positive := []float64{1, 2, 3, 4, 5, 6}
	if got := lag1(positive); got < 0.99 {
		t.Fatalf("lag1 = %v, want strong positive dependence", got)
	}
}

func TestPValueBinsAndQuantiles(t *testing.T) {
	bins, quantiles := pCalibration([]float64{0.005, 0.02, 0.07, 0.2, 0.4, 0.8})
	for i, bin := range bins {
		if bin.Count != 1 {
			t.Fatalf("bin %d count = %d, want 1", i, bin.Count)
		}
	}
	if quantiles[4].Empirical != 0.135 {
		t.Fatalf("median quantile = %v, want 0.135", quantiles[4].Empirical)
	}
}

func syntheticSnapshot(t *testing.T) snapshot {
	t.Helper()
	var history []stats.RunStat
	for i := 0; i < 30; i++ {
		history = append(history, stats.RunStat{RunID: int64(i + 1), Avg: 1_000_000 + float64((i%3)-1)*100})
	}
	baseline, err := stats.ComputeBaseline(history, minPoints, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot{Run: db.Run{ID: 100}}
	for i := 0; i < 100; i++ {
		result := stats.DetectRegression(stats.RunStat{RunID: 100, Avg: 1_000_000}, baseline)
		s.Hypotheses = append(s.Hypotheses, hypothesis{
			Key:        historyKey{"host", "ReleaseFast", "cat", string(rune('a' + i))},
			Evaluation: stats.SnapshotEvaluation{History: history, Baseline: baseline, Result: result},
		})
	}
	return s
}

func TestSyntheticInjectionsAreDeterministicAndEffectsIncreaseDetection(t *testing.T) {
	snapshots := []snapshot{syntheticSnapshot(t)}
	a := injections(snapshots, 42)
	b := injections(snapshots, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different injections")
	}
	if a[3].DetectionRate == nil || a[1].DetectionRate == nil || a[6].DetectionRate == nil || a[4].DetectionRate == nil {
		t.Fatalf("injected scenarios are missing detection rates: %+v", a)
	}
	if *a[3].DetectionRate < *a[1].DetectionRate || *a[6].DetectionRate < *a[4].DetectionRate {
		t.Fatalf("larger effects did not improve detection: %+v", a)
	}
	if a[0].DetectionRate != nil || a[4].FalseAlertRate != nil {
		t.Fatalf("zero-denominator rates must be undefined: %+v", a)
	}
}

func TestDecisionFailsClosed(t *testing.T) {
	decision := decide(Report{})
	if decision.Status != "uncalibrated_regression_score" || decision.FormalGuarantees {
		t.Fatalf("empty evidence did not fail closed: %+v", decision)
	}
}

func TestLegacyComparisonCannotCreateProductionAlert(t *testing.T) {
	s := syntheticSnapshot(t)
	s.Hypotheses = s.Hypotheses[:1]
	h := &s.Hypotheses[0]
	h.Result = db.Result{AvgNs: 1_200_000, P50Ns: 1_000_000, StdDevNs: 100, SampleCount: 3}
	h.Evaluation.Result = stats.DetectRegression(stats.RunStat{RunID: 100, Avg: 1_200_000}, h.Evaluation.Baseline)
	legacyP := 0.5
	h.LegacyP = &legacyP
	h.LegacyOK = false
	applyAlerts(&s)
	if !h.Alert || h.LegacyAlert {
		t.Fatalf("legacy diagnostic affected production result: %+v", h)
	}
}

func TestReportSerialization(t *testing.T) {
	report := buildReport(nil, nil, DefaultOptions(), "input-hash")
	var output bytes.Buffer
	if err := WriteJSON(&output, &report); err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ReportVersion != ReportVersion || decoded.CriteriaVersion != CriteriaVersion || decoded.InputFingerprint != "input-hash" || decoded.Decision.Status != "uncalibrated_regression_score" {
		t.Fatalf("serialized report lost metadata: %+v", decoded)
	}
}
