package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"opentui-bench/internal/jsbench"
	"opentui-bench/internal/record"
)

const (
	JavaScriptTimeout            = 90 * time.Second
	JavaScriptToolTimeout        = 10 * time.Second
	JavaScriptPreparationTimeout = 10 * time.Minute
)

func runJavaScript(ctx context.Context, cfg RunConfig, meta record.RunMetadata, executor Executor) (*record.ParsedRun, error) {
	if err := checkBunVersion(ctx, executor, cfg.RepoPath); err != nil {
		return nil, err
	}
	if cfg.JSRuntime == RuntimeNode {
		if err := checkToolVersion(ctx, executor, cfg.RepoPath, "node", "--version", "v"+jsbench.NodeVersion); err != nil {
			return nil, err
		}
	}
	if err := checkToolVersion(ctx, executor, ZigDir(cfg.RepoPath), "zig", "version", jsbench.ZigVersion); err != nil {
		return nil, err
	}
	if err := runPreparation(ctx, executor, cfg.RepoPath, "bun", "install", "--frozen-lockfile"); err != nil {
		return nil, fmt.Errorf("bun install: %w", err)
	}
	if err := runPreparation(ctx, executor, cfg.RepoPath, "bun", "--no-env-file", "--cwd=packages/core", "run", "build:native"); err != nil {
		return nil, fmt.Errorf("build native: %w", err)
	}

	script := "bench:js"
	if cfg.JSRuntime == RuntimeNode {
		script = "bench:js:node"
	}
	outputs, err := runJavaScriptSamples(ctx, cfg, executor, script)
	if err != nil {
		return nil, err
	}

	invocations := make([]io.Reader, len(outputs))
	for i := range outputs {
		invocations[i] = bytes.NewReader(outputs[i])
	}
	parsed, err := record.ParseJSInvocations(invocations, meta)
	if err != nil {
		return nil, fmt.Errorf("parse JavaScript results: %w", err)
	}
	return parsed, nil
}

func runJavaScriptSamples(ctx context.Context, cfg RunConfig, executor Executor, script string) ([][]byte, error) {
	outputs := make([][]byte, 0, jsbench.Samples)
	for i := 0; i < jsbench.Samples; i++ {
		stdout, stderr, err := runJavaScriptProcess(ctx, cfg, executor, script)
		retried := false
		if err != nil && shouldRetryJavaScriptSample(err, stderr) {
			retried = true
			stdout, stderr, err = runJavaScriptProcess(ctx, cfg, executor, script)
		}
		if err != nil {
			label := fmt.Sprintf("JavaScript sample %d failed", i+1)
			if retried {
				label += " after retry"
			}
			return nil, fmt.Errorf("%s: %w: %s", label, err, strings.TrimSpace(string(stderr)))
		}
		outputs = append(outputs, bytes.Clone(stdout))
	}
	return outputs, nil
}

func runJavaScriptProcess(ctx context.Context, cfg RunConfig, executor Executor, script string) ([]byte, []byte, error) {
	processCtx, cancel := context.WithTimeout(ctx, JavaScriptTimeout)
	defer cancel()
	cmd := exec.Command("bun", "--no-env-file", "--cwd=packages/core", "run", script, "--format=json")
	cmd.Dir = cfg.RepoPath
	cmd.Env = canonicalJavaScriptEnv(os.Environ())
	if cfg.JSRuntime == RuntimeNode {
		cmd.Env = append(cmd.Env, "OTUI_BENCH_NATIVE_PREPARED=1")
	}
	return executor.Output(processCtx, cmd)
}

func shouldRetryJavaScriptSample(err error, stderr []byte) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return bytes.Contains(stderr, []byte("inner RSD ")) && bytes.Contains(stderr, []byte(" exceeds "))
}

func checkBunVersion(ctx context.Context, executor Executor, dir string) error {
	checkCtx, cancel := context.WithTimeout(ctx, JavaScriptToolTimeout)
	defer cancel()
	cmd := exec.Command("bun", "--revision")
	cmd.Dir = dir
	stdout, stderr, err := executor.Output(checkCtx, cmd)
	if err != nil {
		return fmt.Errorf("check bun version: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	revision := strings.TrimSpace(string(stdout))
	version, _, hasRevision := strings.Cut(revision, "+")
	if !hasRevision || version != jsbench.BunVersion {
		return fmt.Errorf("bun revision = %q, want stable %s", revision, jsbench.BunVersion)
	}
	return nil
}

func checkToolVersion(ctx context.Context, executor Executor, dir, name, arg, want string) error {
	checkCtx, cancel := context.WithTimeout(ctx, JavaScriptToolTimeout)
	defer cancel()
	cmd := exec.Command(name, arg)
	cmd.Dir = dir
	stdout, stderr, err := executor.Output(checkCtx, cmd)
	if err != nil {
		return fmt.Errorf("check %s version: %w: %s", name, err, strings.TrimSpace(string(stderr)))
	}
	if got := strings.TrimSpace(string(stdout)); got != want {
		return fmt.Errorf("%s version = %q, want %q", name, got, want)
	}
	return nil
}

func runPreparation(ctx context.Context, executor Executor, dir, name string, args ...string) error {
	preparationCtx, cancel := context.WithTimeout(ctx, JavaScriptPreparationTimeout)
	defer cancel()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	stdout, stderr, err := executor.Output(preparationCtx, cmd)
	if err != nil {
		diagnostics := strings.TrimSpace(string(stderr))
		if diagnostics == "" {
			diagnostics = strings.TrimSpace(string(stdout))
		}
		return fmt.Errorf("%w: %s", err, diagnostics)
	}
	return nil
}

func canonicalJavaScriptEnv(environ []string) []string {
	clean := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OTUI_") || strings.HasPrefix(name, "OPENTUI_") {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}
