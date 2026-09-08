package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildZigBenchCompilesWithoutRunningSample(t *testing.T) {
	runner := &recordingCmdRunner{}
	_, err := BuildZigBench(context.Background(), "/repo/packages/native", "ReleaseFast", runner)
	if err == nil || !strings.Contains(err.Error(), "stop after command capture") {
		t.Fatalf("error = %v, want command capture error", err)
	}
	want := []string{"zig", "build", "bench", "-Dbench-optimize=ReleaseFast", "--verbose", "--", "--help"}
	if !slices.Equal(runner.args, want) {
		t.Fatalf("command = %q, want %q", runner.args, want)
	}
}

type buildOutputRunner struct {
	output   string
	commands [][]string
}

func (r *buildOutputRunner) CombinedOutput(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	r.commands = append(r.commands, slices.Clone(cmd.Args))
	return []byte(r.output), nil
}

func TestBuildZigBenchReturnsBuiltBinary(t *testing.T) {
	zigDir := "/repo/packages/native"
	runner := &buildOutputRunner{
		output: "compile output\n./.zig-cache/o/fast/opentui-bench --help\nUsage: bench [options]\n",
	}
	path, err := BuildZigBench(context.Background(), zigDir, "ReleaseFast", runner)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(zigDir, ".zig-cache/o/fast/opentui-bench")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestBuildZigBenchPreparesVendoredDependencies(t *testing.T) {
	zigDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(zigDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareScript := filepath.Join(zigDir, "scripts", "prepare-zig-deps.sh")
	if err := os.WriteFile(prepareScript, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &buildOutputRunner{
		output: "./.zig-cache/o/fast/opentui-bench --help\n",
	}
	if _, err := BuildZigBench(context.Background(), zigDir, "ReleaseFast", runner); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"sh", prepareScript},
		{"zig", "build", "bench", "-Dbench-optimize=ReleaseFast", "--verbose", "--", "--help"},
	}
	if !slices.EqualFunc(runner.commands, want, slices.Equal[[]string]) {
		t.Fatalf("commands = %q, want %q", runner.commands, want)
	}
}
