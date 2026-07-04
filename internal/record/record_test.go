package record

import (
	"io"
	"math"
	"strings"
	"testing"
)

func TestParseInvocationsPreservesInvocationIndexes(t *testing.T) {
	invocations := []io.Reader{
		strings.NewReader(`{"benchmark":"cat","results":[{"name":"a","min_ns":1,"avg_ns":10,"max_ns":12,"total_ns":20,"iterations":2},{"name":"b","min_ns":1,"avg_ns":20,"max_ns":22,"total_ns":40,"iterations":2}]}`),
		strings.NewReader(`{"benchmark":"cat","results":[{"name":"b","min_ns":1,"avg_ns":22,"max_ns":24,"total_ns":44,"iterations":2}]}`),
		strings.NewReader(`{"benchmark":"cat","results":[{"name":"b","min_ns":1,"avg_ns":24,"max_ns":26,"total_ns":48,"iterations":2},{"name":"a","min_ns":1,"avg_ns":14,"max_ns":16,"total_ns":28,"iterations":2}]}`),
	}
	parsed, err := ParseInvocations(invocations, RunMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(parsed.Results))
	}
	a := parsed.Results[0]
	if a.Name != "a" || len(a.Samples) != 2 || a.Samples[0].SampleIndex != 0 || a.Samples[1].SampleIndex != 2 {
		t.Fatalf("a samples = %+v", a.Samples)
	}
	if *a.SampleAvgVarianceNs2 != 8 {
		t.Fatalf("a variance = %v, want 8", *a.SampleAvgVarianceNs2)
	}
	b := parsed.Results[1]
	if b.Name != "b" || len(b.Samples) != 3 || b.Samples[0].SampleIndex != 0 || b.Samples[1].SampleIndex != 1 || b.Samples[2].SampleIndex != 2 {
		t.Fatalf("b samples = %+v", b.Samples)
	}
}

func TestSampleVariancePreservesFractionalPrecision(t *testing.T) {
	variance := sampleVariance([]int64{1, 2})
	if variance == nil || math.Abs(*variance-0.5) > 1e-12 {
		t.Fatalf("variance = %v, want 0.5", variance)
	}
	result := aggregateSamples("cat", "tiny", []sample{{avgNs: 1}, {index: 1, avgNs: 2}})
	if result.StdDevNs != 0 || result.SampleAvgVarianceNs2 == nil || *result.SampleAvgVarianceNs2 != 0.5 {
		t.Fatalf("stddev/variance = %d/%v", result.StdDevNs, result.SampleAvgVarianceNs2)
	}
}
