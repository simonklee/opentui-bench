package runner

import (
	"bytes"
	"context"
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
	JavaScriptTimeout            = 75 * time.Second
	JavaScriptToolTimeout        = 10 * time.Second
	JavaScriptPreparationTimeout = 10 * time.Minute
)

func runJavaScript(ctx context.Context, cfg RunConfig, meta record.RunMetadata, executor Executor) (*record.ParsedRun, error) {
	if err := checkBunVersion(ctx, executor, cfg.RepoPath); err != nil {
		return nil, err
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

	outputs := make([][]byte, 0, jsbench.Samples)
	for i := 0; i < jsbench.Samples; i++ {
		processCtx, cancel := context.WithTimeout(ctx, JavaScriptTimeout)
		cmd := exec.Command("bun", "--no-env-file", "--cwd=packages/core", "run", "bench:js", "--format=json")
		cmd.Dir = cfg.RepoPath
		cmd.Env = canonicalJavaScriptEnv(os.Environ())
		stdout, stderr, err := executor.Output(processCtx, cmd)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("JavaScript sample %d failed: %w: %s", i+1, err, strings.TrimSpace(string(stderr)))
		}
		outputs = append(outputs, bytes.Clone(stdout))
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
