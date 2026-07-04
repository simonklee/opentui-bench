package broadshift

import (
	"math"
	"testing"

	"opentui-bench/internal/db"
)

func TestDetectUsesExactCompositeIdentityAndAverage(t *testing.T) {
	prior := []db.Result{
		{Category: "render", Name: "shared", AvgNs: 100, P50Ns: 9_999},
		{Category: "layout", Name: "shared", AvgNs: 200, P50Ns: 1},
	}
	target := []db.Result{
		{Category: "render", Name: "shared", AvgNs: 120, P50Ns: 1},
		{Category: "other", Name: "shared", AvgNs: 10_000, P50Ns: 1},
	}

	incident := Detect(target, prior, Config{MinBenchmarks: 1, MinPositiveShare: 1, MinGeometricPct: 10})
	if !incident.Detected || incident.ComparedBenchmarks != 1 {
		t.Fatalf("incident = %+v, want one exact-key comparison", incident)
	}
	if math.Abs(incident.GeometricChangePercent-20) > 1e-9 {
		t.Fatalf("geometric change = %f, want avg_ns change of 20%%", incident.GeometricChangePercent)
	}
	if incident.Cause != CauseUnclassified || incident.Meaning != Meaning {
		t.Fatalf("classification = %+v", incident)
	}
}
