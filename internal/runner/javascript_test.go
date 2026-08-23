package runner

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"opentui-bench/internal/jsbench"
)

type processResult struct {
	stdout []byte
	stderr []byte
	err    error
}

type scriptedExecutor struct {
	commands [][]string
	dirs     []string
	envs     [][]string
	timeouts []time.Duration
	outputs  int
	bun      string
	bench    []processResult
}

func (e *scriptedExecutor) CombinedOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	e.commands = append(e.commands, slices.Clone(cmd.Args))
	e.dirs = append(e.dirs, cmd.Dir)
	e.envs = append(e.envs, slices.Clone(cmd.Env))
	gitOutputs := []string{"abc123", "abc123full", "message", "2026-08-04T00:00:00Z", "main"}
	output := gitOutputs[e.outputs]
	e.outputs++
	return []byte(output), nil
}

func (e *scriptedExecutor) Output(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	e.commands = append(e.commands, slices.Clone(cmd.Args))
	e.dirs = append(e.dirs, cmd.Dir)
	e.envs = append(e.envs, slices.Clone(cmd.Env))
	deadline, ok := ctx.Deadline()
	if !ok {
		e.timeouts = append(e.timeouts, 0)
	} else {
		e.timeouts = append(e.timeouts, time.Until(deadline))
	}
	switch {
	case slices.Equal(cmd.Args, []string{"bun", "--revision"}):
		if e.bun == "" {
			e.bun = jsbench.BunVersion + "+test"
		}
		return []byte(e.bun + "\n"), nil, nil
	case slices.Equal(cmd.Args, []string{"zig", "version"}):
		return []byte(jsbench.ZigVersion + "\n"), nil, nil
	case slices.Equal(cmd.Args, []string{"node", "--version"}):
		return []byte("v" + jsbench.NodeVersion + "\n"), nil, nil
	case len(cmd.Args) > 2 && cmd.Args[2] == "--cwd=packages/core" && cmd.Args[len(cmd.Args)-1] == "--format=json":
		if len(e.bench) == 0 {
			return nil, []byte("unstable case"), errors.New("exit status 1")
		}
		result := e.bench[0]
		e.bench = e.bench[1:]
		return result.stdout, result.stderr, result.err
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
		{"bun", "--revision"},
		{"zig", "version"},
		{"bun", "install", "--frozen-lockfile"},
		{"bun", "--no-env-file", "--cwd=packages/core", "run", "build:native"},
		{"bun", "--no-env-file", "--cwd=packages/core", "run", "bench:js", "--format=json"},
	}
	if !slices.EqualFunc(executor.commands, want, func(a, b []string) bool { return slices.Equal(a, b) }) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, want)
	}
	for i, dir := range executor.dirs {
		wantDir := "/repo"
		if slices.Equal(executor.commands[i], []string{"zig", "version"}) {
			wantDir = "/repo/packages/native"
		}
		if dir != wantDir {
			t.Fatalf("command %q directory = %q, want %q", executor.commands[i], dir, wantDir)
		}
	}
	timeouts := []time.Duration{
		JavaScriptToolTimeout,
		JavaScriptToolTimeout,
		JavaScriptPreparationTimeout,
		JavaScriptPreparationTimeout,
		JavaScriptTimeout,
	}
	for i, timeout := range timeouts {
		if executor.timeouts[i] <= 0 || executor.timeouts[i] > timeout {
			t.Fatalf("command %q timeout = %s, want within %s", executor.commands[i+5], executor.timeouts[i], timeout)
		}
	}
}

func TestJavaScriptRetriesInnerRSDOnce(t *testing.T) {
	ok := processResult{stdout: []byte(`{"ok":true}`)}
	executor := &scriptedExecutor{bench: []processResult{
		{stderr: []byte("JS Mouse/stdin-sgr-bubble-depth-8: inner RSD 134621 ppm exceeds 50000 ppm\n"), err: errors.New("exit status 1")},
		ok, ok, ok,
	}}
	outputs, err := runJavaScriptSamples(context.Background(), RunConfig{RepoPath: "/repo"}, executor, "bench:js")
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != jsbench.Samples || string(outputs[0]) != `{"ok":true}` {
		t.Fatalf("outputs = %q", outputs)
	}
	if got := countBenchCommands(executor); got != jsbench.Samples+1 {
		t.Fatalf("process attempts = %d, want %d", got, jsbench.Samples+1)
	}
}

func TestJavaScriptReportsInnerRSDAfterRetry(t *testing.T) {
	failure := processResult{
		stderr: []byte("JS Text/text-buffer-word-wrap-measure: inner RSD 59864 ppm exceeds 50000 ppm\n"),
		err:    errors.New("exit status 1"),
	}
	executor := &scriptedExecutor{bench: []processResult{failure, failure}}
	_, err := runJavaScriptSamples(context.Background(), RunConfig{RepoPath: "/repo"}, executor, "bench:js")
	if err == nil || !strings.Contains(err.Error(), "JavaScript sample 1 failed after retry") ||
		!strings.Contains(err.Error(), "inner RSD 59864 ppm") {
		t.Fatalf("error = %v", err)
	}
	if got := countBenchCommands(executor); got != 2 {
		t.Fatalf("process attempts = %d, want 2", got)
	}
}

func TestJavaScriptDoesNotRetryOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result processResult
	}{
		{
			name:   "non-rsd",
			result: processResult{stderr: []byte("lockfile had changes, but lockfile is frozen\n"), err: errors.New("exit status 1")},
		},
		{
			name: "canceled",
			result: processResult{
				stderr: []byte("JS Mouse/stdin-sgr-bubble-depth-8: inner RSD 134621 ppm exceeds 50000 ppm\n"),
				err:    context.Canceled,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &scriptedExecutor{bench: []processResult{tc.result, {stdout: []byte(`{"ok":true}`)}}}
			_, err := runJavaScriptSamples(context.Background(), RunConfig{RepoPath: "/repo"}, executor, "bench:js")
			if err == nil || strings.Contains(err.Error(), "after retry") {
				t.Fatalf("error = %v", err)
			}
			if tc.result.err == context.Canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want canceled", err)
			}
			if got := countBenchCommands(executor); got != 1 {
				t.Fatalf("process attempts = %d, want 1", got)
			}
		})
	}
}

func countBenchCommands(executor *scriptedExecutor) int {
	count := 0
	for _, command := range executor.commands {
		if len(command) > 0 && command[len(command)-1] == "--format=json" {
			count++
		}
	}
	return count
}

func TestJavaScriptRejectsPrereleaseBun(t *testing.T) {
	executor := &scriptedExecutor{bun: jsbench.BunVersion + "-canary.1+test"}
	err := checkBunVersion(context.Background(), executor, "/repo")
	if err == nil || !strings.Contains(err.Error(), "want stable "+jsbench.BunVersion) {
		t.Fatalf("error = %v, want stable Bun rejection", err)
	}
}

func TestNodeOrchestrationVerifiesPinAndUsesNodeScript(t *testing.T) {
	executor := &scriptedExecutor{}
	cfg := RunConfig{RepoPath: "/repo", BenchmarkKind: BenchmarkJS, JSRuntime: RuntimeNode, Samples: 3, Profile: ProfileNone}
	_, _, err := RunAndCollectWithExecutor(context.Background(), cfg, executor)
	if err == nil || !strings.Contains(err.Error(), "unstable case") {
		t.Fatalf("error = %v", err)
	}
	if !slices.ContainsFunc(executor.commands, func(command []string) bool {
		return slices.Equal(command, []string{"node", "--version"})
	}) || !slices.ContainsFunc(executor.commands, func(command []string) bool {
		return slices.Equal(command, []string{"bun", "--no-env-file", "--cwd=packages/core", "run", "bench:js:node", "--format=json"})
	}) {
		t.Fatalf("commands = %#v", executor.commands)
	}
	for i, command := range executor.commands {
		if len(command) >= 2 && command[len(command)-2] == "bench:js:node" && !slices.Contains(executor.envs[i], "OTUI_BENCH_NATIVE_PREPARED=1") {
			t.Fatalf("Node benchmark environment = %q", executor.envs[i])
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

func TestZigConfigRejectsJavaScriptIdentity(t *testing.T) {
	cfg := normalizeRunConfig(RunConfig{BenchmarkKind: BenchmarkZig, JSRuntime: RuntimeBun})
	if err := validateRunConfig(cfg); err == nil || !strings.Contains(err.Error(), "must not include JavaScript identity") {
		t.Fatalf("validateRunConfig error = %v", err)
	}
}

func TestJavaScriptPreparationKillsProcessGroupOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runPreparation(ctx, OSRunner{}, t.TempDir(), "sh", "-c", "sleep 30 & wait")
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
