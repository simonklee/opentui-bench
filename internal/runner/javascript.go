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

	"opentui-bench/internal/record"
)

const (
	JavaScriptSuite        = "core-default"
	JavaScriptProtocol     = int64(1)
	JavaScriptBunVersion   = "1.3.14"
	JavaScriptZigVersion   = "0.15.2"
	JavaScriptManifestHash = "sha256:0fa487783682b1227bfd4bf735fe1a969ea03f045bb8a68f87c1e41174cb3794"
	JavaScriptSamples      = 3
	JavaScriptTimeout      = 75 * time.Second
)

func runJavaScript(ctx context.Context, cfg RunConfig, meta record.RunMetadata, executor Executor) (*record.ParsedRun, error) {
	if err := checkToolVersion(ctx, executor, cfg.RepoPath, "bun", "--version", JavaScriptBunVersion); err != nil {
		return nil, err
	}
	if err := checkToolVersion(ctx, executor, cfg.RepoPath, "zig", "version", JavaScriptZigVersion); err != nil {
		return nil, err
	}
	if err := runPreparation(ctx, executor, cfg.RepoPath, "bun", "install", "--frozen-lockfile"); err != nil {
		return nil, fmt.Errorf("bun install: %w", err)
	}
	if err := runPreparation(ctx, executor, cfg.RepoPath, "bun", "--cwd=packages/core", "run", "build:native"); err != nil {
		return nil, fmt.Errorf("build native: %w", err)
	}

	outputs := make([][]byte, 0, JavaScriptSamples)
	for i := 0; i < JavaScriptSamples; i++ {
		processCtx, cancel := context.WithTimeout(ctx, JavaScriptTimeout)
		cmd := exec.Command("bun", "--cwd=packages/core", "run", "bench:js", "--format=json")
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

func checkToolVersion(ctx context.Context, executor Executor, dir, name, arg, want string) error {
	cmd := exec.CommandContext(ctx, name, arg)
	cmd.Dir = dir
	stdout, stderr, err := executor.Output(ctx, cmd)
	if err != nil {
		return fmt.Errorf("check %s version: %w: %s", name, err, strings.TrimSpace(string(stderr)))
	}
	if got := strings.TrimSpace(string(stdout)); got != want {
		return fmt.Errorf("%s version = %q, want %q", name, got, want)
	}
	return nil
}

func runPreparation(ctx context.Context, executor Executor, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	stdout, stderr, err := executor.Output(ctx, cmd)
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
