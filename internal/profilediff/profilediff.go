package profilediff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/pprof/profile"
)

// Quality describes whether a stored pprof blob supports sample-count comparisons.
type Quality struct {
	SampleCount            int64  `json:"sample_count"`
	FunctionCount          int    `json:"function_count"`
	UnresolvedFrameCount   int    `json:"unresolved_frame_count"`
	ResolvedFrameCount     int    `json:"resolved_frame_count"`
	SampleType             string `json:"sample_type"`
	SampleUnit             string `json:"sample_unit"`
	Period                 int64  `json:"period"`
	PeriodType             string `json:"period_type"`
	DurationNanos          int64  `json:"duration_nanos"`
	WeightQuantity         string `json:"weight_quantity"`
	WeightUnit             string `json:"weight_unit"`
	CPUNanosecondsReliable bool   `json:"cpu_nanoseconds_reliable"`
	CaptureScope           string `json:"capture_scope"`
	Insufficient           bool   `json:"insufficient"`
	InsufficientReason     string `json:"insufficient_reason,omitempty"`
}

type FunctionShare struct {
	Name            string  `json:"name"`
	BaselineSamples int64   `json:"baseline_samples"`
	TargetSamples   int64   `json:"target_samples"`
	BaselineShare   float64 `json:"baseline_share"`
	TargetShare     float64 `json:"target_share"`
	SampleDelta     int64   `json:"sample_delta"`
	ShareDelta      float64 `json:"share_delta"`
	Quantity        string  `json:"quantity"`
}

func IsResolvedFunction(name string) bool {
	return name != "" && name != "[unknown]" && name != "??" && !strings.HasPrefix(name, "0x")
}

func Inspect(data []byte) (Quality, error) {
	_, quality, err := functionSamples(data)
	return quality, err
}

func functionSamples(data []byte) (map[string]int64, Quality, error) {
	prof, err := profile.ParseData(data)
	if err != nil {
		return nil, Quality{}, fmt.Errorf("parse pprof: %w", err)
	}
	q := Quality{
		CaptureScope:   "whole-process",
		WeightQuantity: "unsupported",
	}
	if prof.DurationNanos > 0 {
		q.DurationNanos = prof.DurationNanos
	}
	if prof.Period > 0 {
		q.Period = prof.Period
	}
	if prof.PeriodType != nil {
		q.PeriodType = prof.PeriodType.Type + "/" + prof.PeriodType.Unit
	}
	sampleIndex := -1
	if len(prof.SampleType) > 0 {
		q.SampleType = prof.SampleType[0].Type
		q.SampleUnit = prof.SampleType[0].Unit
		for i, sampleType := range prof.SampleType {
			if sampleType.Type == "samples" && sampleType.Unit == "count" {
				sampleIndex = i
				q.SampleType = sampleType.Type
				q.SampleUnit = sampleType.Unit
				q.WeightQuantity = "sample_count"
				break
			}
		}
	}
	q.WeightUnit = q.SampleUnit
	q.FunctionCount = len(prof.Function)
	counts := map[string]int64{}
	negativeCounts := false
	for _, sample := range prof.Sample {
		var weight int64
		if sampleIndex >= 0 {
			weight = sample.Value[sampleIndex]
			if weight < 0 {
				negativeCounts = true
			}
			if weight <= 0 {
				continue
			}
			q.SampleCount += weight
		}
		seen := map[string]bool{}
		for _, loc := range sample.Location {
			if len(loc.Line) == 0 {
				q.UnresolvedFrameCount++
				continue
			}
			for _, line := range loc.Line {
				if line.Function == nil || !IsResolvedFunction(line.Function.Name) {
					q.UnresolvedFrameCount++
					continue
				}
				q.ResolvedFrameCount++
				name := line.Function.Name
				if weight > 0 && !seen[name] {
					seen[name] = true
					counts[name] += weight
				}
			}
		}
	}
	if sampleIndex < 0 {
		q.InsufficientReason = "profile has no samples/count metric"
	} else if negativeCounts {
		q.SampleCount = 0
		q.InsufficientReason = "profile has negative sample counts"
	} else if q.SampleCount == 0 {
		q.InsufficientReason = "pprof contains no positive sample counts"
	} else if q.ResolvedFrameCount == 0 {
		q.InsufficientReason = "pprof contains no resolved stack frames"
	}
	q.Insufficient = q.InsufficientReason != ""
	if q.Insufficient {
		return nil, q, nil
	}
	return counts, q, nil
}

func Diff(baseline, target []byte, limit int) ([]FunctionShare, Quality, Quality, error) {
	baseCounts, baseQuality, err := functionSamples(baseline)
	if err != nil {
		return nil, Quality{}, Quality{}, err
	}
	targetCounts, targetQuality, err := functionSamples(target)
	if err != nil {
		return nil, Quality{}, Quality{}, err
	}
	if baseQuality.Insufficient || targetQuality.Insufficient {
		return nil, baseQuality, targetQuality, nil
	}
	names := map[string]struct{}{}
	for name := range baseCounts {
		names[name] = struct{}{}
	}
	for name := range targetCounts {
		names[name] = struct{}{}
	}
	diff := make([]FunctionShare, 0, len(names))
	for name := range names {
		base := baseCounts[name]
		targ := targetCounts[name]
		share := FunctionShare{
			Name:            name,
			BaselineSamples: base,
			TargetSamples:   targ,
			SampleDelta:     targ - base,
			Quantity:        "sample_count",
		}
		share.BaselineShare = float64(base) / float64(baseQuality.SampleCount)
		share.TargetShare = float64(targ) / float64(targetQuality.SampleCount)
		share.ShareDelta = share.TargetShare - share.BaselineShare
		diff = append(diff, share)
	}
	sort.Slice(diff, func(i, j int) bool {
		ai, aj := abs64(diff[i].SampleDelta), abs64(diff[j].SampleDelta)
		if ai != aj {
			return ai > aj
		}
		return diff[i].Name < diff[j].Name
	})
	if limit > 0 && len(diff) > limit {
		diff = diff[:limit]
	}
	return diff, baseQuality, targetQuality, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
