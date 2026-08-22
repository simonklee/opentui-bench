package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ZigDir(repoPath string) string {
	nativeDir := filepath.Join(repoPath, "packages", "native")
	if _, err := os.Stat(filepath.Join(nativeDir, "build.zig")); err == nil {
		return nativeDir
	}
	legacyDir := filepath.Join(repoPath, "packages", "core", "src", "zig")
	if _, err := os.Stat(filepath.Join(legacyDir, "build.zig")); err == nil {
		return legacyDir
	}
	return nativeDir
}

func BuildZigBench(ctx context.Context, zigDir string, optimize string, r CmdRunner) (string, error) {
	cmd := exec.CommandContext(ctx, "zig", "build", "bench", "-Dbench-optimize="+optimize, "--verbose", "--", "--help")
	cmd.Dir = zigDir

	out, err := r.CombinedOutput(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("zig build failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	const suffix = "opentui-bench --help"
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, suffix) {
			continue
		}
		path := strings.TrimSpace(strings.TrimSuffix(line, "--help"))
		if !filepath.IsAbs(path) {
			path = filepath.Join(zigDir, path)
		}
		return filepath.Clean(path), nil
	}
	return "", fmt.Errorf("zig build did not report benchmark binary path")
}

func RunZigBenchJSON(ctx context.Context, zigDir string, optimize string, args []string, r CmdRunner) ([]byte, error) {
	cmdArgs := []string{"build", "bench", "-Doptimize=" + optimize, "--", "--json", "--mem"}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "zig", cmdArgs...)
	cmd.Dir = zigDir

	out, err := r.CombinedOutput(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("zig bench failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return out, nil
}
