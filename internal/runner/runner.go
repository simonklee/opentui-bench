package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/record"
)

type ProfileMode string

const (
	ProfileNone ProfileMode = "none"
	ProfileCPU  ProfileMode = "cpu"
)

type RunConfig struct {
	RepoPath        string
	ZigOptimize     string
	Filter          string
	FilterBenchmark string
	Benchmarks      []string
	Samples         int
	Profile         ProfileMode
	PerfFreq        int
	Notes           string
	MachineID       string
	WorkDir         string
}

func Run(ctx context.Context, database *db.DB, cfg RunConfig) (int64, error) {
	if cfg.Samples < 1 {
		return 0, fmt.Errorf("samples must be >= 1")
	}
	if cfg.Profile == ProfileCPU && cfg.PerfFreq <= 0 {
		cfg.PerfFreq = 997
	}

	runner := OSRunner{}

	meta, err := ReadGitMeta(ctx, cfg.RepoPath, runner)
	if err != nil {
		return 0, fmt.Errorf("read git meta: %w", err)
	}

	if cfg.Notes != "" {
		meta.Notes = cfg.Notes
	}
	if cfg.MachineID != "" {
		meta.MachineID = cfg.MachineID
	}
	meta.ZigOptimize = cfg.ZigOptimize
	meta.SampleCount = cfg.Samples

	zigDir := ZigDir(cfg.RepoPath)
	var args []string
	if cfg.Filter != "" {
		args = append(args, "--filter", cfg.Filter)
	}
	if cfg.FilterBenchmark != "" {
		args = append(args, "--bench", cfg.FilterBenchmark)
	}

	err = BuildZigBench(ctx, zigDir, cfg.ZigOptimize, runner)
	if err != nil {
		return 0, fmt.Errorf("build failed: %w", err)
	}

	benchBin, err := FindBenchmarkBinary(zigDir)
	if err != nil {
		return 0, fmt.Errorf("find benchmark binary: %w", err)
	}

	var buf bytes.Buffer
	for i := 0; i < cfg.Samples; i++ {
		cmdArgs := []string{"--json", "--mem"}
		cmdArgs = append(cmdArgs, args...)

		cmd := exec.CommandContext(ctx, benchBin, cmdArgs...)
		cmd.Dir = zigDir
		out, err := runner.CombinedOutput(ctx, cmd)
		if err != nil {
			return 0, fmt.Errorf("sample %d failed: %w", i+1, err)
		}

		buf.Write(out)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}

	runID, count, err := record.Record(database, bytes.NewReader(buf.Bytes()), meta)
	if err != nil {
		return 0, fmt.Errorf("record results: %w", err)
	}

	_ = count

	if cfg.Profile == ProfileCPU {
		if cfg.ZigOptimize != "ReleaseSafe" {
			err = BuildZigBench(ctx, zigDir, "ReleaseSafe", runner)
			if err != nil {
				return runID, fmt.Errorf("profiling build failed: %w", err)
			}
			benchBin, err = FindBenchmarkBinary(zigDir)
			if err != nil {
				return runID, fmt.Errorf("find benchmark binary (safe): %w", err)
			}
		}

		results, err := database.GetResultsForRun(runID)
		if err != nil {
			return runID, fmt.Errorf("get results for profiling: %w", err)
		}

		for _, res := range results {
			pbGz, kind, err := CaptureCPUProfile(ctx, runner, benchBin, res.Name, cfg.PerfFreq)
			if err != nil {
				return runID, fmt.Errorf("profile %s: %w", res.Name, err)
			}

			_, err = database.InsertArtifact(&db.Artifact{
				ResultID:  res.ID,
				Kind:      kind,
				DataBlob:  pbGz,
				Metadata:  fmt.Sprintf(`{"perf_freq":%d}`, cfg.PerfFreq),
				CreatedAt: time.Now().Format(time.RFC3339),
			})
			if err != nil {
				fmt.Printf("Warning: failed to store artifact for %s: %v\n", res.Name, err)
			}
		}
	}

	return runID, nil
}
