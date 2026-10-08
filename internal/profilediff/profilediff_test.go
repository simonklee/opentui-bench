package profilediff

import (
	"bytes"
	"testing"

	"github.com/google/pprof/profile"
)

func testProfile(t *testing.T, fn, sampleType, unit string, weights ...int64) []byte {
	t.Helper()
	fnObj := &profile.Function{ID: 1, Name: fn}
	loc := &profile.Location{ID: 1, Line: []profile.Line{{Function: fnObj}}}
	p := &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: sampleType, Unit: unit}},
		Function:      []*profile.Function{fnObj},
		Location:      []*profile.Location{loc},
		Period:        997000,
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		DurationNanos: 1000000000,
	}
	for _, weight := range weights {
		p.Sample = append(p.Sample, &profile.Sample{Location: []*profile.Location{loc}, Value: []int64{weight}})
	}
	return encodeProfile(t, p)
}

func encodeProfile(t *testing.T, p *profile.Profile) []byte {
	t.Helper()
	if err := p.CheckValid(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiffReportsSampleCountsNotNanoseconds(t *testing.T) {
	baseline := testProfile(t, "work", "samples", "count", 10)
	target := testProfile(t, "work", "samples", "count", 20)
	diff, baseQ, targQ, err := Diff(baseline, target, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].Quantity != "sample_count" || diff[0].SampleDelta != 10 {
		t.Fatalf("diff = %+v", diff)
	}
	if baseQ.SampleCount != 10 || targQ.SampleCount != 20 {
		t.Fatalf("quality samples = %d/%d", baseQ.SampleCount, targQ.SampleCount)
	}
	if baseQ.CPUNanosecondsReliable || targQ.CPUNanosecondsReliable || baseQ.WeightUnit != "count" {
		t.Fatalf("quality = %+v / %+v", baseQ, targQ)
	}
}

func TestUnsupportedMetrics(t *testing.T) {
	counts := testProfile(t, "work", "samples", "count", 10)
	for _, metric := range []profile.ValueType{
		{Type: "cpu", Unit: "nanoseconds"},
		{Type: "samples", Unit: "nanoseconds"},
		{Type: "cpu", Unit: "count"},
		{Type: "alloc_space", Unit: "bytes"},
	} {
		t.Run(metric.Type+"/"+metric.Unit, func(t *testing.T) {
			data := testProfile(t, "work", metric.Type, metric.Unit, 9970000)
			q, err := Inspect(data)
			if err != nil {
				t.Fatal(err)
			}
			if !q.Insufficient || q.InsufficientReason != "profile has no samples/count metric" || q.SampleCount != 0 {
				t.Fatalf("quality = %+v", q)
			}
			if q.SampleType != metric.Type || q.SampleUnit != metric.Unit || q.WeightQuantity != "unsupported" || q.WeightUnit != metric.Unit || q.CPUNanosecondsReliable {
				t.Fatalf("metric metadata = %+v", q)
			}
			if q.ResolvedFrameCount != 1 || q.Period != 997000 || q.PeriodType != "cpu/nanoseconds" || q.DurationNanos != 1000000000 {
				t.Fatalf("capture metadata = %+v", q)
			}
			for _, pair := range [][2][]byte{{data, data}, {counts, data}, {data, counts}} {
				diff, baseQ, targetQ, err := Diff(pair[0], pair[1], 10)
				if err != nil || len(diff) != 0 || (!baseQ.Insufficient && !targetQ.Insufficient) {
					t.Fatalf("unsupported diff = %+v, %+v, %+v, %v", diff, baseQ, targetQ, err)
				}
			}
		})
	}
}

func TestZeroAndNegativeCounts(t *testing.T) {
	baseline := testProfile(t, "work", "samples", "count", 10)
	for _, weights := range [][]int64{{0}, {-1}, {10, -1}} {
		data := testProfile(t, "work", "samples", "count", weights...)
		diff, _, q, err := Diff(baseline, data, 10)
		if err != nil || len(diff) != 0 || !q.Insufficient || q.SampleCount != 0 || q.InsufficientReason == "" {
			t.Fatalf("weights %v: diff = %+v, quality = %+v, err = %v", weights, diff, q, err)
		}
	}
	target := testProfile(t, "work", "samples", "count", 0, 10)
	diff, _, q, err := Diff(baseline, target, 10)
	if err != nil || q.Insufficient || q.SampleCount != 10 || q.ResolvedFrameCount != 1 || len(diff) != 1 || diff[0].SampleDelta != 0 {
		t.Fatalf("zero-count row: diff = %+v, quality = %+v, err = %v", diff, q, err)
	}
}

func TestSelectsCountMetricAndIgnoresUnresolvedFrames(t *testing.T) {
	fn := &profile.Function{ID: 1, Name: "work"}
	address := &profile.Function{ID: 2, Name: "0x1234"}
	unknown := &profile.Function{ID: 3, Name: "[unknown]"}
	loc := &profile.Location{ID: 1, Line: []profile.Line{{Function: fn}, {Function: fn}, {Function: address}, {Function: unknown}}}
	p := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}, {Type: "samples", Unit: "count"}},
		Function:   []*profile.Function{fn, address, unknown},
		Location:   []*profile.Location{loc},
		Sample:     []*profile.Sample{{Location: []*profile.Location{loc}, Value: []int64{9970000, 10}}},
	}
	data := encodeProfile(t, p)
	diff, q, _, err := Diff(data, data, 10)
	if err != nil || q.Insufficient || q.SampleCount != 10 || q.SampleType != "samples" || q.SampleUnit != "count" || q.UnresolvedFrameCount != 2 || q.ResolvedFrameCount != 2 {
		t.Fatalf("quality = %+v, err = %v", q, err)
	}
	if len(diff) != 1 || diff[0].Name != "work" || diff[0].BaselineSamples != 10 || diff[0].BaselineShare != 1 {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestDiffNormalizesZigAnonymousInstantiationSuffixes(t *testing.T) {
	baseline := testProfile(t, "text-buffer-view.UnifiedTextBufferView.calculateVirtualLinesGeneric__anon_79190.WrapContext.line_end_callback", "samples", "count", 104)
	target := testProfile(t, "text-buffer-view.UnifiedTextBufferView.calculateVirtualLinesGeneric__anon_73384.WrapContext.line_end_callback", "samples", "count", 158)
	diff, _, _, err := Diff(baseline, target, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := "text-buffer-view.UnifiedTextBufferView.calculateVirtualLinesGeneric.WrapContext.line_end_callback"
	if len(diff) != 1 || diff[0].Name != want || diff[0].BaselineSamples != 104 || diff[0].TargetSamples != 158 || diff[0].SampleDelta != 54 {
		t.Fatalf("diff = %+v", diff)
	}
}
