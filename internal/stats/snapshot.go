package stats

import (
	"sort"
	"time"
)

// OrderedRunStat attaches the stable run ordering key to a benchmark observation.
type OrderedRunStat struct {
	RunDate time.Time
	Stat    RunStat
}

type SnapshotConfig struct {
	Window         int
	MinPoints      int
	BaselineOffset int
}

type SnapshotEvaluation struct {
	History  []RunStat
	Baseline *BaselineStats
	Result   RegressionResult
}

// EvaluateSnapshot evaluates one target using only observations strictly before
// it in (run instant, run ID) order. Offset and window are target-relative.
func EvaluateSnapshot(target OrderedRunStat, observations []OrderedRunStat, config SnapshotConfig) SnapshotEvaluation {
	prior := make([]OrderedRunStat, 0, len(observations))
	for _, observation := range observations {
		if observation.Stat.RunID == target.Stat.RunID {
			continue
		}
		if observation.RunDate.Before(target.RunDate) ||
			(observation.RunDate.Equal(target.RunDate) && observation.Stat.RunID < target.Stat.RunID) {
			prior = append(prior, observation)
		}
	}

	sort.SliceStable(prior, func(i, j int) bool {
		if prior[i].RunDate.Equal(prior[j].RunDate) {
			return prior[i].Stat.RunID > prior[j].Stat.RunID
		}
		return prior[i].RunDate.After(prior[j].RunDate)
	})

	offset := config.BaselineOffset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(prior) {
		return SnapshotEvaluation{Result: RegressionResult{Status: "insufficient"}}
	}
	prior = prior[offset:]
	if config.Window > 0 && len(prior) > config.Window {
		prior = prior[:config.Window]
	}

	history := make([]RunStat, len(prior))
	for i := range prior {
		history[i] = prior[i].Stat
	}
	baseline, err := ComputeBaseline(history, config.MinPoints, 0)
	if err != nil {
		return SnapshotEvaluation{History: history, Result: RegressionResult{Status: "insufficient"}}
	}

	return SnapshotEvaluation{
		History:  history,
		Baseline: baseline,
		Result:   DetectRegression(target.Stat, baseline),
	}
}
