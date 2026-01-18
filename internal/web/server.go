package web

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"opentui-bench/internal/cache"
	"opentui-bench/internal/db"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	db            *db.DB
	addr          string
	svgCache      *cache.SVGCache
	flamegraphSem chan struct{}
	pprofManager  *PProfManager
}

func NewServer(database *db.DB, addr string) *Server {
	cacheDir := os.Getenv("SVG_CACHE_DIR")
	if cacheDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, ".cache", "opentui-bench", "svg")
		} else {
			cacheDir = "/data/svg-cache"
		}
	}

	maxRuns := 5
	if envMax := os.Getenv("SVG_CACHE_MAX_RUNS"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxRuns = n
		}
	}

	maxConcurrency := 2
	if envMax := os.Getenv("FLAMEGRAPH_MAX_CONCURRENCY"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxConcurrency = n
		}
	}

	svgCache, err := cache.NewSVGCache(cacheDir, maxRuns)
	if err != nil {
		fmt.Printf("Warning: failed to initialize SVG cache: %v\n", err)
	}

	return &Server{
		db:            database,
		addr:          addr,
		svgCache:      svgCache,
		flamegraphSem: make(chan struct{}, maxConcurrency),
		pprofManager:  NewPProfManager(),
	}
}

func (s *Server) Start(openBrowser bool) error {
	mux := http.NewServeMux()

	appFS, err := fs.Sub(staticFiles, "static/app")
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}
	mux.Handle("/", spaFileServer(appFS))

	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/runs/", s.routeRunsAPI)
	mux.HandleFunc("/api/compare", s.handleCompare)
	mux.HandleFunc("/api/trend", s.handleTrend)
	mux.HandleFunc("/api/benchmarks", s.handleBenchmarks)
	mux.HandleFunc("/api/regressions", s.handleRegressions)
	mux.HandleFunc("/api/database/download", s.handleDatabaseDownload)

	if openBrowser {
		url := fmt.Sprintf("http://localhost%s", s.addr)
		go openURL(url)
	}

	fmt.Printf("Starting server at http://localhost%s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}

func spaFileServer(appFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(appFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")

		// Try to serve the file if it exists and is not a directory
		if path != "" {
			info, err := fs.Stat(appFS, path)
			if err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Fallback to index.html for SPA routing (and root)
		// We manually serve the content to avoid http.FileServer's redirect loops
		// when it sees a request for "/index.html"
		f, err := appFS.Open("index.html")
		if err != nil {
			http.Error(w, "index.html missing", http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()

		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "index.html stat failed", http.StatusInternalServerError)
			return
		}

		// Prevent caching of index.html so updates are seen immediately
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		if rs, ok := f.(io.ReadSeeker); ok {
			http.ServeContent(w, r, "index.html", stat.ModTime(), rs)
		} else {
			http.Error(w, "internal error: file not seekable", http.StatusInternalServerError)
		}
	})
}

func (s *Server) routeRunsAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case strings.HasSuffix(path, "/flamegraphs"):
		s.handleFlamegraphList(w, r)
	case strings.Contains(path, "/results/") && strings.Contains(path, "/pprof/ui"):
		s.handlePProfUI(w, r)
	case strings.Contains(path, "/results/") && strings.HasSuffix(path, "/flamegraph"):
		s.handleFlamegraphSVG(w, r)
	case strings.Contains(path, "/results/") && strings.HasSuffix(path, "/callgraph"):
		s.handleCallgraphSVG(w, r)
	case strings.HasSuffix(path, "/categories"):
		s.handleCategories(w, r)
	case strings.HasSuffix(path, "/artifacts"):
		s.handleArtifactList(w, r)
	case strings.HasSuffix(path, "/download") && strings.Contains(path, "/artifacts/"):
		s.handleArtifactDownload(w, r)
	default:
		s.handleRun(w, r)
	}
}
