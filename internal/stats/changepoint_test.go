package stats

import "testing"

func makeSeries(values []float64) []RunStat {
	series := make([]RunStat, len(values))
	for i, v := range values {
		series[i] = RunStat{
			RunID: int64(i + 1),
			Avg:   v,
		}
	}
	return series
}

func TestDetectChangePointsStepChange(t *testing.T) {
	series := makeSeries([]float64{100, 100, 100, 100, 100, 200, 200, 200, 200, 200})
	points := DetectChangePoints(series, 2, 0.05, 199)

	if len(points) != 1 {
		t.Fatalf("expected 1 change point, got %d", len(points))
	}
	if points[0].Index != 5 {
		t.Fatalf("expected change point at index 5, got %d", points[0].Index)
	}
	if points[0].RunID != series[5].RunID {
		t.Fatalf("expected RunID %d, got %d", series[5].RunID, points[0].RunID)
	}
}

func TestDetectChangePointsNoChange(t *testing.T) {
	series := makeSeries([]float64{100, 101, 99, 100, 102, 98, 100, 101, 99, 100})
	points := DetectChangePoints(series, 2, 0.05, 199)

	if len(points) != 0 {
		t.Fatalf("expected no change points, got %d", len(points))
	}
}

func TestDetectChangePointsTwoShifts(t *testing.T) {
	series := makeSeries([]float64{100, 100, 100, 100, 100, 200, 200, 200, 200, 200, 300, 300, 300, 300, 300})
	points := DetectChangePoints(series, 2, 0.05, 199)

	if len(points) != 2 {
		t.Fatalf("expected 2 change points, got %d", len(points))
	}
	if points[0].Index != 5 || points[1].Index != 10 {
		t.Fatalf("expected change points at indexes [5, 10], got [%d, %d]", points[0].Index, points[1].Index)
	}
}

func TestDetectChangePointsTooFewPoints(t *testing.T) {
	series := makeSeries([]float64{100, 100, 100, 200, 200, 200, 200})
	points := DetectChangePoints(series, 4, 0.05, 199)
	if points != nil {
		t.Fatalf("expected nil for too-few points, got %#v", points)
	}
}

func TestDetectChangePointsGradualDriftNoPanic(t *testing.T) {
	series := makeSeries([]float64{100, 102, 104, 106, 108, 110, 112, 114, 116, 118})
	_ = DetectChangePoints(series, 2, 0.05, 199)
}
