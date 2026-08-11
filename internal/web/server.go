package web

import (
	"crypto/subtle"
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
	"opentui-bench/internal/joblease"
	"opentui-bench/internal/jsbench"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	db                  *db.DB
	addr                string
	apiKey              string
	javascriptRuns      bool
	svgCache            *cache.SVGCache
	profileRetention    db.ProfileRetention
	flamegraphSem       chan struct{}
	databaseDownloadSem chan struct{}
	pprofManager        *PProfManager
}

func NewServer(database *db.DB, addr string) (*Server, error) {
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

	profileRunsMax, err := positiveIntEnv("PROFILE_RETENTION_RUNS", db.DefaultProfileRunsMax)
	if err != nil {
		return nil, err
	}
	profileMiBMax, err := positiveIntEnv("PROFILE_RETENTION_MIB", int(db.DefaultProfileBytesMax>>20))
	if err != nil {
		return nil, err
	}
	if profileMiBMax > 1<<20 {
		return nil, fmt.Errorf("PROFILE_RETENTION_MIB must not exceed %d", 1<<20)
	}

	svgCache, err := cache.NewSVGCache(cacheDir, maxRuns)
	if err != nil {
		return nil, fmt.Errorf("initialize SVG cache: %w", err)
	}

	return &Server{
		db:             database,
		addr:           addr,
		apiKey:         os.Getenv("BENCH_API_KEY"),
		javascriptRuns: os.Getenv("BENCH_ENABLE_JAVASCRIPT_RUNS") == "1",
		svgCache:       svgCache,
		profileRetention: db.ProfileRetention{
			MaxRuns:  profileRunsMax,
			MaxBytes: int64(profileMiBMax) << 20,
		},
		flamegraphSem:       make(chan struct{}, maxConcurrency),
		databaseDownloadSem: make(chan struct{}, 1),
		pprofManager:        NewPProfManager(),
	}, nil
}

func positiveIntEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

// requireAuth wraps a handler to require bearer token authentication.
// If no API key is configured (dev mode), all requests are allowed.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			next(w, r) // no auth configured (dev mode)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) Start(openBrowser bool) error {
	pruned, err := s.pruneProfileData()
	if err != nil {
		return fmt.Errorf("prune profile data: %w", err)
	}
	if pruned.BlobsDeleted > 0 {
		fmt.Printf("Pruned %d profile blobs (%.1f MiB)\n",
			pruned.BlobsDeleted, float64(pruned.BytesDeleted)/(1<<20))
	}

	mux := http.NewServeMux()

	appFS, err := fs.Sub(staticFiles, "static/app")
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}
	mux.Handle("/", spaFileServer(appFS))

	mux.HandleFunc("/api/runs", s.handleRunsRoute)
	mux.HandleFunc("/api/runs/", s.routeRunsAPI)
	mux.HandleFunc("/api/compare", s.handleCompare)
	mux.HandleFunc("/api/trend", s.handleTrend)
	mux.HandleFunc("/api/runtime-compare", s.handleRuntimeCompare)
	mux.HandleFunc("/api/runtime-trend", s.handleRuntimeTrend)
	mux.HandleFunc("/api/benchmarks", s.handleBenchmarks)
	mux.HandleFunc("/api/regressions/history", s.handleRegressionsHistory)
	mux.HandleFunc("/api/regressions", s.handleRegressions)
	mux.HandleFunc("/api/branches", s.handleBranches)
	mux.HandleFunc("/api/capabilities", s.handleCapabilities)
	mux.HandleFunc("/api/has-commit/", s.handleHasCommit)
	mux.HandleFunc("/api/latest-commit", s.handleLatestCommit)
	mux.HandleFunc("/api/jobs", s.handleJobsRoute)
	mux.HandleFunc("/api/jobs/", s.routeJobsAPI)
	mux.HandleFunc("/api/database/download", s.handleDatabaseDownload)

	if openBrowser {
		url := fmt.Sprintf("http://localhost%s", s.addr)
		go openURL(url)
	}

	fmt.Printf("Starting server at http://localhost%s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	javascriptRuns := 0
	if s.javascriptRuns {
		javascriptRuns = 1
	}
	_, _ = fmt.Fprintf(w, `{"javascript_runs":%d,"javascript_runtimes":["bun","node"],"javascript_protocol":%d,"javascript_manifest_hash":%q,"job_lease_protocol":%d}`, javascriptRuns, 1, jsbench.ManifestDigest, joblease.Protocol)
}

func (s *Server) pruneProfileData() (db.ProfileRetentionResult, error) {
	return s.db.PruneProfileData(s.profileRetentionConfig())
}

func (s *Server) profileRetentionConfig() db.ProfileRetention {
	retention := s.profileRetention
	if retention.MaxRuns == 0 {
		retention.MaxRuns = db.DefaultProfileRunsMax
	}
	if retention.MaxBytes == 0 {
		retention.MaxBytes = db.DefaultProfileBytesMax
	}
	return retention
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
	case strings.HasSuffix(path, "/artifacts/finalize") && r.Method == http.MethodPost:
		s.requireAuth(s.handleFinalizeArtifacts)(w, r)
	case strings.Contains(path, "/results/") && strings.HasSuffix(path, "/artifacts") && r.Method == http.MethodPost:
		s.requireAuth(s.handleUploadArtifact)(w, r)
	case strings.HasSuffix(path, "/artifacts"):
		s.handleArtifactList(w, r)
	case strings.HasSuffix(path, "/download") && strings.Contains(path, "/artifacts/"):
		s.handleArtifactDownload(w, r)
	default:
		s.handleRun(w, r)
	}
}
