package runner

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestBuildZigBenchCompilesWithoutRunningSample(t *testing.T) {
	runner := &recordingCmdRunner{}
	err := BuildZigBench(context.Background(), "/repo/packages/native", "ReleaseSafe", runner)
	if err == nil || !strings.Contains(err.Error(), "stop after command capture") {
		t.Fatalf("error = %v, want command capture error", err)
	}
	want := []string{"zig", "build", "bench", "-Dbench-optimize=ReleaseSafe", "--", "--help"}
	if !slices.Equal(runner.args, want) {
		t.Fatalf("command = %q, want %q", runner.args, want)
	}
}
