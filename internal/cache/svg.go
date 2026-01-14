package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type SVGCache struct {
	cacheDir string
	maxRuns  int
}

func NewSVGCache(cacheDir string, maxRuns int) (*SVGCache, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &SVGCache{
		cacheDir: cacheDir,
		maxRuns:  maxRuns,
	}, nil
}

func (c *SVGCache) runDir(runID int64) string {
	return filepath.Join(c.cacheDir, fmt.Sprintf("run-%d", runID))
}

func (c *SVGCache) svgPath(runID int64, benchmarkName string) string {
	hash := sha256.Sum256([]byte(benchmarkName))
	filename := hex.EncodeToString(hash[:8]) + ".svg"
	return filepath.Join(c.runDir(runID), filename)
}

func (c *SVGCache) Get(runID int64, benchmarkName string) ([]byte, bool) {
	path := c.svgPath(runID, benchmarkName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *SVGCache) Put(runID int64, benchmarkName string, svg []byte) error {
	dir := c.runDir(runID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create run cache dir: %w", err)
	}

	path := c.svgPath(runID, benchmarkName)
	tmp, err := os.CreateTemp(dir, "svg-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp svg: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if err := tmp.Chmod(0644); err != nil {
		return fmt.Errorf("chmod temp svg: %w", err)
	}
	if n, err := tmp.Write(svg); err != nil {
		return fmt.Errorf("write temp svg: %w", err)
	} else if n < len(svg) {
		return fmt.Errorf("write temp svg: short write")
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp svg: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(path)
		if err := os.Rename(tmp.Name(), path); err != nil {
			return fmt.Errorf("rename svg: %w", err)
		}
	}
	return nil
}

func (c *SVGCache) GetOrGenerate(runID int64, benchmarkName string, foldedStacks string) ([]byte, error) {
	if svg, ok := c.Get(runID, benchmarkName); ok {
		return svg, nil
	}

	svg, err := GenerateSVG(foldedStacks, benchmarkName)
	if err != nil {
		return nil, err
	}

	_ = c.Put(runID, benchmarkName, svg)

	return svg, nil
}

func GenerateSVG(foldedStacks, title string) ([]byte, error) {
	cmd := exec.Command("inferno-flamegraph", "--title", title)
	cmd.Stdin = strings.NewReader(foldedStacks)
	svg, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inferno-flamegraph: %w", err)
	}
	return svg, nil
}

func (c *SVGCache) PruneOldRuns(keepRunIDs []int64) error {
	if c.maxRuns > 0 && len(keepRunIDs) > c.maxRuns {
		sort.Slice(keepRunIDs, func(i, j int) bool {
			return keepRunIDs[i] > keepRunIDs[j]
		})
		keepRunIDs = keepRunIDs[:c.maxRuns]
	}

	keepSet := make(map[int64]bool)
	for _, id := range keepRunIDs {
		keepSet[id] = true
	}

	entries, err := os.ReadDir(c.cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cache dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}

		runIDStr := strings.TrimPrefix(entry.Name(), "run-")
		runID, err := strconv.ParseInt(runIDStr, 10, 64)
		if err != nil {
			continue
		}

		if !keepSet[runID] {
			runDir := filepath.Join(c.cacheDir, entry.Name())
			if err := os.RemoveAll(runDir); err != nil {
				return fmt.Errorf("remove run-%d cache: %w", runID, err)
			}
		}
	}

	return nil
}

func (c *SVGCache) DeleteRun(runID int64) error {
	dir := c.runDir(runID)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove run cache: %w", err)
	}
	return nil
}

func (c *SVGCache) CacheDir() string {
	return c.cacheDir
}
