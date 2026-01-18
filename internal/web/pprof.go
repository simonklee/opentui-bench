package web

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/pprof/driver"
)

var errProfileTooLarge = errors.New("profile too large")

type pprofSession struct {
	handlers   map[string]http.Handler
	tempFile   string
	lastAccess time.Time
	active     int
}

type PProfManager struct {
	mu          sync.Mutex
	loadMu      sync.Mutex
	sessions    map[string]*pprofSession
	lastCleanup time.Time
}

func NewPProfManager() *PProfManager {
	return &PProfManager{
		sessions: make(map[string]*pprofSession),
	}
}

const (
	pprofSessionTTL      = 30 * time.Minute
	pprofCleanupInterval = 5 * time.Minute
)

func (s *Server) handlePProfUI(w http.ResponseWriter, r *http.Request) {
	// Path: /api/runs/{run_id}/results/{result_id}/pprof/ui/...
	path := r.URL.Path

	// We need to parse runID and resultID.
	// Since the path structure is fixed, we can try to extract them.
	// Prefix: /api/runs/
	if !strings.HasPrefix(path, "/api/runs/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	trimmed := strings.TrimPrefix(path, "/api/runs/")
	parts := strings.SplitN(trimmed, "/results/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}

	// parts[1] starts with {resultID}/pprof/ui...
	remaining := parts[1]
	slashIdx := strings.Index(remaining, "/")
	if slashIdx == -1 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	resultIDStr := remaining[:slashIdx]
	resultID, err := strconv.ParseInt(resultIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid result id", http.StatusBadRequest)
		return
	}

	// Check if this result belongs to the run
	if err := s.ensureResultBelongsToRun(runID, resultID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "result not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// We need the artifact content to pass to serve
	// But serve only needs it if the session is not created.
	// To avoid fetching the blob every time, we should check if session exists first.
	// But PProfManager hides the session logic.
	// We can add Exists() method or just fetch it lazily inside serve?
	// But `serve` is in PProfManager which doesn't know about DB.
	// So we should fetch it if needed.
	// Let's modify serve signature to accept a "fetcher" function?
	// Or simply fetch it. Profiles are max 50MB, usually smaller.
	// But fetching 50MB from SQLite for every request (CSS, JS, etc) is bad.

	// Better: PProfManager.serve should take a `func() ([]byte, error)` provider.
	// And it only calls it if needed.

	fetcher := func() ([]byte, error) {
		artifact, err := s.db.GetArtifact(resultID, cpuProfileKind)
		if err != nil {
			return nil, err
		}
		if len(artifact.DataBlob) > maxProfileSize {
			return nil, errProfileTooLarge
		}
		return artifact.DataBlob, nil
	}

	s.pprofManager.serve(w, r, runID, resultID, fetcher)
}

func (pm *PProfManager) serve(w http.ResponseWriter, r *http.Request, runID, resultID int64, fetcher func() ([]byte, error)) {
	key := fmt.Sprintf("%d:%d", runID, resultID)

	now := time.Now()
	pm.mu.Lock()
	pm.cleanupLocked(now)
	sess, exists := pm.sessions[key]
	if exists {
		sess.lastAccess = now
		sess.active++
	}
	pm.mu.Unlock()

	if !exists {
		// Fetch data only now
		data, err := fetcher()
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "profile artifact not found", http.StatusNotFound)
			} else if errors.Is(err, errProfileTooLarge) {
				http.Error(w, "profile too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// Create new session
		tmp, err := os.CreateTemp("", "pprof-*.pb.gz")
		if err != nil {
			http.Error(w, "failed to create temp file", http.StatusInternalServerError)
			return
		}
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			http.Error(w, "failed to write profile", http.StatusInternalServerError)
			return
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmp.Name())
			http.Error(w, "failed to finalize profile", http.StatusInternalServerError)
			return
		}

		pm.loadMu.Lock()
		handlers, err := loadPProfHandlers(tmp.Name())
		pm.loadMu.Unlock()
		if err != nil {
			_ = os.Remove(tmp.Name())
			http.Error(w, fmt.Sprintf("failed to load pprof: %v", err), http.StatusInternalServerError)
			return
		}

		newSess := &pprofSession{
			handlers:   handlers,
			tempFile:   tmp.Name(),
			lastAccess: now,
			active:     1,
		}

		pm.mu.Lock()
		// Double check locking
		if existing, ok := pm.sessions[key]; ok {
			// Race condition lost, use existing
			_ = os.Remove(tmp.Name()) // Clean up our unused temp
			sess = existing
			sess.lastAccess = now
			sess.active++
		} else {
			pm.sessions[key] = newSess
			sess = newSess
		}
		pm.mu.Unlock()
	}

	defer func() {
		pm.mu.Lock()
		if sess.active > 0 {
			sess.active--
		}
		pm.mu.Unlock()
	}()

	prefix := fmt.Sprintf("/api/runs/%d/results/%d/pprof/ui", runID, resultID)
	// Canonicalize path: if we are at prefix, redirect to prefix/
	if r.URL.Path == prefix {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
		return
	}

	subpath := strings.TrimPrefix(r.URL.Path, prefix)
	if subpath == "" {
		subpath = "/"
	}

	// Disasm requires access to the original binary which we don't have.
	// Return a helpful error instead of pprof's confusing "no matches found for regexp".
	if subpath == "/disasm" {
		http.Error(w, "Disassembly view requires the original binary file, which is not available. Use the flamegraph, top, or source views instead.", http.StatusNotImplemented)
		return
	}

	handler, ok := sess.handlers[subpath]
	if !ok {
		// Try to find if pprof handles this path differently?
		// pprof usually registers explicit paths.
		// If subpath is "/flamegraph", it matches.
		// If subpath is "/flamegraph/"?

		http.NotFound(w, r)
		return
	}

	req := r.Clone(r.Context())
	req.URL.Path = subpath
	req.URL.RawPath = ""
	handler.ServeHTTP(w, req)
}

func (pm *PProfManager) cleanupLocked(now time.Time) {
	if !pm.lastCleanup.IsZero() && now.Sub(pm.lastCleanup) < pprofCleanupInterval {
		return
	}
	pm.lastCleanup = now

	for key, sess := range pm.sessions {
		if sess.active > 0 {
			continue
		}
		if now.Sub(sess.lastAccess) <= pprofSessionTTL {
			continue
		}
		delete(pm.sessions, key)
		if sess.tempFile != "" {
			_ = os.Remove(sess.tempFile)
		}
	}
}

func loadPProfHandlers(profilePath string) (map[string]http.Handler, error) {
	flags := &mockFlagSet{
		bools:   make(map[string]*bool),
		ints:    make(map[string]*int),
		floats:  make(map[string]*float64),
		strings: make(map[string]*string),
	}

	var handlers map[string]http.Handler

	options := &driver.Options{
		Flagset: flags,
		UI:      discardUI{},
		HTTPServer: func(args *driver.HTTPServerArgs) error {
			handlers = args.Handlers
			return nil
		},
	}

	flags.args = []string{profilePath}

	if err := driver.PProf(options); err != nil {
		return nil, err
	}

	if handlers == nil {
		return nil, fmt.Errorf("failed to capture handlers")
	}

	return handlers, nil
}

type mockFlagSet struct {
	bools   map[string]*bool
	ints    map[string]*int
	floats  map[string]*float64
	strings map[string]*string
	args    []string
}

type discardUI struct{}

func (discardUI) ReadLine(string) (string, error) { return "", io.EOF }
func (discardUI) Print(...interface{})            {}
func (discardUI) PrintErr(...interface{})         {}
func (discardUI) IsTerminal() bool                { return false }
func (discardUI) WantBrowser() bool               { return false }
func (discardUI) SetAutoComplete(func(string) string) {
}

func (f *mockFlagSet) Bool(name string, def bool, usage string) *bool {
	b := def
	f.bools[name] = &b
	return &b
}

func (f *mockFlagSet) Int(name string, def int, usage string) *int {
	i := def
	f.ints[name] = &i
	return &i
}

func (f *mockFlagSet) Float64(name string, def float64, usage string) *float64 {
	fl := def
	f.floats[name] = &fl
	return &fl
}

func (f *mockFlagSet) String(name string, def string, usage string) *string {
	s := def
	f.strings[name] = &s
	return &s
}

func (f *mockFlagSet) StringList(name string, def string, usage string) *[]*string {
	v := []*string{}
	return &v
}
func (f *mockFlagSet) ExtraUsage() string      { return "" }
func (f *mockFlagSet) AddExtraUsage(eu string) {}
func (f *mockFlagSet) Parse(usage func()) []string {
	if httpFlag, ok := f.strings["http"]; ok {
		*httpFlag = "localhost:0"
	}
	return f.args
}
