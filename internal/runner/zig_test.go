package runner

import (
	"context"
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
	output string
}

func (r buildOutputRunner) CombinedOutput(_ context.Context, _ *exec.Cmd) ([]byte, error) {
	return []byte(r.output), nil
}

func TestBuildZigBenchReturnsBuiltBinary(t *testing.T) {
	zigDir := "/repo/packages/native"
	path, err := BuildZigBench(context.Background(), zigDir, "ReleaseFast", buildOutputRunner{
		output: "compile output\n./.zig-cache/o/fast/opentui-bench --help\nUsage: bench [options]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(zigDir, ".zig-cache/o/fast/opentui-bench")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}
