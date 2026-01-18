package runner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func ZigDir(repoPath string) string {
	return filepath.Join(repoPath, "packages", "core", "src", "zig")
}

func BuildZigBench(ctx context.Context, zigDir string, optimize string, r CmdRunner) error {
	cmd := exec.CommandContext(ctx, "zig", "build", "-Doptimize="+optimize)
	cmd.Dir = zigDir

	out, err := r.CombinedOutput(ctx, cmd)
	if err != nil {
		return fmt.Errorf("zig build failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func FindBenchmarkBinary(zigDir string) (string, error) {
	// Try standard install location first (from zig build)
	binPath := filepath.Join(zigDir, "zig-out", "bin", "opentui-bench")
	if info, err := os.Stat(binPath); err == nil && !info.IsDir() {
		return binPath, nil
	}

	// Fallback: search zig-cache (legacy behavior)
	cacheDir := filepath.Join(zigDir, ".zig-cache")
	var newestPath string
	var newestTime time.Time

	err := filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "opentui-bench" {
			info, err := d.Info()
			if err == nil && info.Mode()&0o111 != 0 {
				if info.ModTime().After(newestTime) {
					newestTime = info.ModTime()
					newestPath = path
				}
			}
		}
		return nil
	})

	if newestPath != "" {
		return newestPath, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return "", fmt.Errorf("opentui-bench binary not found in %s", cacheDir)
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
