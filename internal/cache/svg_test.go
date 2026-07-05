package cache

import (
	"os"
	"testing"
	"time"
)

func TestSVGCacheEnforcesRecentRunBound(t *testing.T) {
	cache, err := NewSVGCache(t.TempDir(), 2)
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}
	if err := cache.Put(1, "first", []byte("one")); err != nil {
		t.Fatalf("put run 1: %v", err)
	}
	if err := cache.Put(2, "second", []byte("two")); err != nil {
		t.Fatalf("put run 2: %v", err)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cache.runDir(2), old, old); err != nil {
		t.Fatalf("age run 2: %v", err)
	}
	if _, ok := cache.Get(1, "first"); !ok {
		t.Fatal("get run 1: cache miss")
	}
	if err := cache.Put(3, "third", []byte("three")); err != nil {
		t.Fatalf("put run 3: %v", err)
	}

	if _, ok := cache.Get(1, "first"); !ok {
		t.Error("recently used run 1 was pruned")
	}
	if _, ok := cache.Get(2, "second"); ok {
		t.Error("least recently used run 2 was retained")
	}
	if _, ok := cache.Get(3, "third"); !ok {
		t.Error("new run 3 was pruned")
	}
}

func TestSVGCachePrunesExistingDirectoriesAtStartup(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"run-1", "run-2", "run-3"} {
		if err := os.MkdirAll(dir+"/"+name, 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir+"/run-1", old, old); err != nil {
		t.Fatal(err)
	}

	cache, err := NewSVGCache(dir, 2)
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}
	if _, err := os.Stat(cache.runDir(1)); !os.IsNotExist(err) {
		t.Errorf("old run directory stat error = %v, want not exist", err)
	}
}
