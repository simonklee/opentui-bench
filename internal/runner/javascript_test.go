package runner

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type scriptedExecutor struct {
	commands [][]string
	dirs     []string
	outputs  int
}

func (e *scriptedExecutor) CombinedOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	e.commands = append(e.commands, slices.Clone(cmd.Args))
	e.dirs = append(e.dirs, cmd.Dir)
	gitOutputs := []string{"abc123", "abc123full", "message", "2026-08-04T00:00:00Z", "main"}
	output := gitOutputs[e.outputs]
	e.outputs++
	return []byte(output), nil
}

func (e *scriptedExecutor) Output(_ context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	e.commands = append(e.commands, slices.Clone(cmd.Args))
	e.dirs = append(e.dirs, cmd.Dir)
	switch {
	case slices.Equal(cmd.Args, []string{"bun", "--version"}):
		return []byte(JavaScriptBunVersion + "\n"), nil, nil
	case slices.Equal(cmd.Args, []string{"zig", "version"}):
		return []byte(JavaScriptZigVersion + "\n"), nil, nil
	case len(cmd.Args) > 1 && cmd.Args[1] == "--cwd=packages/core" && cmd.Args[len(cmd.Args)-1] == "--format=json":
		return nil, []byte("unstable case"), errors.New("exit status 1")
	default:
		return nil, nil, nil
	}
}

func TestJavaScriptOrchestrationUsesCanonicalCommandsAndSeparateDiagnostics(t *testing.T) {
	executor := &scriptedExecutor{}
	cfg := RunConfig{RepoPath: "/repo", BenchmarkKind: BenchmarkJS, Samples: 3, Profile: ProfileNone}
	_, _, err := RunAndCollectWithExecutor(context.Background(), cfg, executor)
	if err == nil || !strings.Contains(err.Error(), "unstable case") {
		t.Fatalf("error = %v, want separate stderr diagnostics", err)
	}
	want := [][]string{
		{"git", "rev-parse", "--short", "HEAD"},
		{"git", "rev-parse", "HEAD"},
		{"git", "log", "-1", "--format=%s"},
		{"git", "log", "-1", "--format=%cI"},
		{"git", "branch", "--show-current"},
		{"bun", "--version"},
		{"zig", "version"},
		{"bun", "install", "--frozen-lockfile"},
		{"bun", "--cwd=packages/core", "run", "build:native"},
		{"bun", "--cwd=packages/core", "run", "bench:js", "--format=json"},
	}
	if !slices.EqualFunc(executor.commands, want, func(a, b []string) bool { return slices.Equal(a, b) }) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, want)
	}
	for _, dir := range executor.dirs {
		if dir != "/repo" {
			t.Fatalf("command directory = %q, want /repo", dir)
		}
	}
}

func TestJavaScriptConfigRejectsNonCanonicalOptions(t *testing.T) {
	base := normalizeRunConfig(RunConfig{BenchmarkKind: BenchmarkJS, Samples: 3, Profile: ProfileNone})
	if err := validateRunConfig(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RunConfig){
		func(cfg *RunConfig) { cfg.Samples = 2 },
		func(cfg *RunConfig) { cfg.Profile = ProfileCPU },
		func(cfg *RunConfig) { cfg.Filter = "layout" },
		func(cfg *RunConfig) { cfg.ManifestHash = "sha256:other" },
	} {
		cfg := base
		mutate(&cfg)
		if err := validateRunConfig(cfg); err == nil {
			t.Fatalf("accepted noncanonical config: %+v", cfg)
		}
	}
}

func TestOSRunnerOutputKillsProcessGroupOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := (OSRunner{}).Output(ctx, exec.Command("sh", "-c", "sleep 30 & wait"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("process group cleanup took %s", time.Since(start))
	}
}

func TestCanonicalJavaScriptEnvClearsOpenTUIOverrides(t *testing.T) {
	got := canonicalJavaScriptEnv([]string{"PATH=/bin", "OTUI_ASSET_ROOT=/tmp", "OPENTUI_LIBC=musl", "HOME=/home/test"})
	want := []string{"PATH=/bin", "HOME=/home/test"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}
