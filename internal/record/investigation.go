package record

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"opentui-bench/internal/db"
)

const (
	MaxInvestigationRecordingBytes = 48 << 20
	maxInvestigationArtifactBytes  = 32 << 20
	maxInvestigationMetadataBytes  = 16 << 10
	maxInvestigationRecipeBytes    = 64 << 10
)

type InvestigationRecording struct {
	AttemptKey string             `json:"attempt_key"`
	Recipe     json.RawMessage    `json:"recipe"`
	Runs       []InvestigationRun `json:"runs"`
}

type InvestigationRun struct {
	Run       ParsedRun               `json:"run"`
	Artifacts []InvestigationArtifact `json:"artifacts,omitempty"`
}

type InvestigationArtifact struct {
	Kind     string `json:"kind"`
	Data     []byte `json:"data"`
	Metadata string `json:"metadata"`
}

func (recording InvestigationRecording) Validate() error {
	if err := db.ValidateAttemptKey(recording.AttemptKey); err != nil {
		return err
	}
	if len(recording.Runs) < 2 || len(recording.Runs) > 3 {
		return fmt.Errorf("an investigation recording requires two or three runs")
	}
	if len(recording.Recipe) > maxInvestigationRecipeBytes || !json.Valid(recording.Recipe) {
		return fmt.Errorf("recipe must be valid JSON of at most %d bytes", maxInvestigationRecipeBytes)
	}
	roles := make(map[string]bool)
	artifactBytes := 0
	for _, side := range recording.Runs {
		meta := side.Run.Meta
		if meta.Purpose != db.PurposeInvestigation || meta.AttemptKey != recording.AttemptKey || meta.BenchmarkKind != "zig" {
			return fmt.Errorf("each run must belong to this Zig investigation attempt")
		}
		if meta.AttemptRole != db.AttemptRoleBaseline && meta.AttemptRole != db.AttemptRoleTarget && meta.AttemptRole != db.AttemptRoleCandidate {
			return fmt.Errorf("invalid recording role %q", meta.AttemptRole)
		}
		if roles[meta.AttemptRole] {
			return fmt.Errorf("duplicate recording role %q", meta.AttemptRole)
		}
		roles[meta.AttemptRole] = true
		metadata, err := json.Marshal(meta)
		if err != nil || len(metadata) > maxInvestigationMetadataBytes {
			return fmt.Errorf("run metadata exceeds %d bytes", maxInvestigationMetadataBytes)
		}
		if meta.SampleCount < 1 || meta.SampleCount > db.MaxInvestigationSamples {
			return fmt.Errorf("samples must be between 1 and %d", db.MaxInvestigationSamples)
		}
		if len(side.Run.Results) != 1 {
			return fmt.Errorf("each investigation run must contain exactly one result")
		}
		result := side.Run.Results[0]
		if result.Category == "" || result.Name == "" || len(result.Category) > 1024 || len(result.Name) > 1024 {
			return fmt.Errorf("invalid investigation result selector")
		}
		if result.SampleCount != int64(meta.SampleCount) || len(result.Samples) != meta.SampleCount {
			return fmt.Errorf("result must contain all %d invocation samples", meta.SampleCount)
		}
		for i, sample := range result.Samples {
			if sample.SampleIndex != int64(i) || sample.AvgNs <= 0 || len(sample.Batches) != 0 {
				return fmt.Errorf("invalid Zig invocation sample %d", i)
			}
		}
		if v := result.SampleAvgVarianceNs2; v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return fmt.Errorf("sample variance must be finite and nonnegative")
		}
		if len(result.MemStats) > 64 {
			return fmt.Errorf("too many memory statistics")
		}
		for _, stat := range result.MemStats {
			if len(stat.Name) > 1024 {
				return fmt.Errorf("memory statistic name exceeds 1024 bytes")
			}
		}
		if len(side.Artifacts) > 1 {
			return fmt.Errorf("each investigation run accepts at most one CPU profile")
		}
		for _, artifact := range side.Artifacts {
			if artifact.Kind != "cpu.pprof" || len(artifact.Data) == 0 {
				return fmt.Errorf("investigation artifacts must be nonempty CPU profiles")
			}
			if len(artifact.Metadata) > maxInvestigationMetadataBytes || (artifact.Metadata != "" && !json.Valid([]byte(artifact.Metadata))) {
				return fmt.Errorf("artifact metadata must be valid JSON of at most %d bytes", maxInvestigationMetadataBytes)
			}
			if len(artifact.Data) > maxInvestigationArtifactBytes-artifactBytes {
				return fmt.Errorf("investigation artifacts exceed %d bytes", maxInvestigationArtifactBytes)
			}
			artifactBytes += len(artifact.Data)
		}
	}
	if !roles[db.AttemptRoleBaseline] || !roles[db.AttemptRoleTarget] {
		return fmt.Errorf("baseline and target recordings are required")
	}
	return nil
}

func StoreInvestigation(database *db.DB, recording InvestigationRecording, retention db.ProfileRetention) (*db.InvestigationAttempt, bool, error) {
	if err := recording.Validate(); err != nil {
		return nil, false, err
	}
	runs := make([]db.AttemptRun, 0, len(recording.Runs))
	for _, side := range recording.Runs {
		run, results := storageValues(&side.Run)
		stored := db.AttemptRun{Run: run, Results: results}
		for _, artifact := range side.Artifacts {
			stored.Artifacts = append(stored.Artifacts, db.Artifact{
				Kind: artifact.Kind, DataBlob: artifact.Data, Metadata: artifact.Metadata,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}
		runs = append(runs, stored)
	}
	return database.RecordAttempt(recording.AttemptKey, string(recording.Recipe), runs, retention)
}
