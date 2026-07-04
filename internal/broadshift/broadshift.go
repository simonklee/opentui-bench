package broadshift

import (
	"math"

	"opentui-bench/internal/db"
)

const (
	CauseUnclassified = "unclassified"
	Meaning           = "many benchmarks moved together; cause unknown"
)

type Config struct {
	MinBenchmarks    int
	MinPositiveShare float64
	MinGeometricPct  float64
}

type Incident struct {
	Detected               bool    `json:"detected"`
	Cause                  string  `json:"cause"`
	PositiveShare          float64 `json:"positive_share"`
	GeometricChangePercent float64 `json:"geometric_change_percent"`
	ComparedBenchmarks     int     `json:"compared_benchmarks"`
	Meaning                string  `json:"meaning"`
}

func Empty() Incident {
	return Incident{Cause: CauseUnclassified, Meaning: Meaning}
}

// Detect compares exact benchmark identities between a target and its
// immediate prior compatible run. avg_ns is the only measurement used.
func Detect(target []db.Result, prior []db.Result, config Config) Incident {
	incident := Empty()
	priorByKey := make(map[db.BenchmarkKey]int64, len(prior))
	for _, result := range prior {
		priorByKey[db.BenchmarkKey{Category: result.Category, Name: result.Name}] = result.AvgNs
	}

	positive := 0
	logSum := 0.0
	for _, result := range target {
		priorAvg, ok := priorByKey[db.BenchmarkKey{Category: result.Category, Name: result.Name}]
		if !ok || priorAvg <= 0 || result.AvgNs <= 0 {
			continue
		}
		incident.ComparedBenchmarks++
		if result.AvgNs > priorAvg {
			positive++
		}
		logSum += math.Log(float64(result.AvgNs) / float64(priorAvg))
	}
	if incident.ComparedBenchmarks == 0 {
		return incident
	}

	incident.PositiveShare = float64(positive) / float64(incident.ComparedBenchmarks)
	incident.GeometricChangePercent = (math.Exp(logSum/float64(incident.ComparedBenchmarks)) - 1) * 100
	incident.Detected = incident.ComparedBenchmarks >= config.MinBenchmarks &&
		incident.PositiveShare >= config.MinPositiveShare &&
		incident.GeometricChangePercent >= config.MinGeometricPct
	return incident
}
