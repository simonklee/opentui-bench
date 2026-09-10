package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"opentui-bench/internal/record"
)

var (
	ErrUnsupportedHarness = errors.New("harness cannot isolate the requested workload")
	ErrMismatchedWorkload = errors.New("captured output did not match the requested workload")
)

type HarnessCapability struct {
	SupportsJSON   bool `json:"supports_json"`
	SupportsBench  bool `json:"supports_bench"`
	SupportsFilter bool `json:"supports_filter"`
}

type Recipe struct {
	CommitHash       string     `json:"commit_hash"`
	ExecutableSHA256 string     `json:"executable_sha256"`
	BuildCommands    [][]string `json:"build_commands"`
	TimingCommand    []string   `json:"timing_command"`
	ProfileCommand   []string   `json:"profile_command,omitempty"`
	WorkingDir       string     `json:"working_dir"`
	ZigOptimize      string     `json:"zig_optimize"`
	SourceDir        string     `json:"source_dir"`
	Harness          string     `json:"harness"`
	Filter           string     `json:"filter,omitempty"`
	Bench            string     `json:"bench,omitempty"`
	Samples          int        `json:"samples"`
	Profile          string     `json:"profile"`
	PerfFreq         int        `json:"perf_freq,omitempty"`
	CaptureScope     string     `json:"capture_scope"`
	ZigVersion       string     `json:"zig_version,omitempty"`
	SelectorUnique   bool       `json:"selector_unique"`
}

func ProbeHarness(ctx context.Context, benchBin, workingDir string, executor Executor) (HarnessCapability, error) {
	cmd := exec.CommandContext(ctx, benchBin, "--help")
	cmd.Dir = workingDir
	out, err := executor.CombinedOutput(ctx, cmd)
	help := string(out)
	if err != nil && !strings.Contains(strings.ToLower(help), "usage") {
		return HarnessCapability{}, fmt.Errorf("probe harness: %w\n%s", err, strings.TrimSpace(help))
	}
	lower := strings.ToLower(help)
	return HarnessCapability{
		SupportsJSON:   strings.Contains(lower, "--json"),
		SupportsBench:  strings.Contains(lower, "--bench"),
		SupportsFilter: strings.Contains(lower, "--filter"),
	}, nil
}

func (c HarnessCapability) CanIsolate(category, name string) error {
	if !c.SupportsJSON {
		return fmt.Errorf("%w: missing --json", ErrUnsupportedHarness)
	}
	if !c.SupportsFilter || !c.SupportsBench {
		return fmt.Errorf("%w: --filter and --bench are both required", ErrUnsupportedHarness)
	}
	if category == "" && name == "" {
		return fmt.Errorf("%w: empty selector", ErrUnsupportedHarness)
	}
	return nil
}

func (c HarnessCapability) HarnessName() string {
	switch {
	case c.SupportsJSON && c.SupportsBench:
		return "native-json-bench"
	case c.SupportsJSON && c.SupportsFilter:
		return "legacy-json-filter"
	default:
		return "unsupported"
	}
}

func selectedWorkload(parsed *record.ParsedRun, category, name string) (*record.ParsedResult, error) {
	if len(parsed.Results) != 1 {
		return nil, fmt.Errorf("%w: got %d results for %s/%s", ErrMismatchedWorkload, len(parsed.Results), category, name)
	}
	result := &parsed.Results[0]
	if result.Category != category || result.Name != name {
		return nil, fmt.Errorf("%w: got %s/%s, want %s/%s", ErrMismatchedWorkload, result.Category, result.Name, category, name)
	}
	return result, nil
}
