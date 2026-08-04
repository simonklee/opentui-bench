package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"opentui-bench/internal/db"
)

func TestCreateRunOldClientStoresLegacyProvenance(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	server := &Server{db: database}
	body := `{"commit_hash":"abc","results":[{"category":"cat","name":"bench","min_ns":1,"avg_ns":2,"max_ns":3,"total_ns":2,"iterations":1,"sample_count":3}]}`
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	results, err := database.GetResultsForRun(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SampleDataVersion != 0 || results[0].SummaryVersion != 1 || results[0].SampleAvgVarianceNs2 != nil || len(results[0].Samples) != 0 {
		t.Fatalf("legacy result = %+v", results)
	}
}

func TestCreateZigRunValidatesTimedOrUntimedShape(t *testing.T) {
	server, _ := newAPIStorageServer(t)
	for _, test := range []struct {
		name   string
		result string
		want   int
	}{
		{"timed", `{"category":"cat","name":"timed","min_ns":1,"avg_ns":2,"max_ns":3,"total_ns":2,"iterations":1,"sample_count":1}`, http.StatusCreated},
		{"untimed", `{"category":"cat","name":"untimed","iterations":10,"sample_count":3}`, http.StatusCreated},
		{"mixed", `{"category":"cat","name":"mixed","avg_ns":2,"iterations":1,"sample_count":1}`, http.StatusBadRequest},
		{"negative", `{"category":"cat","name":"negative","iterations":-1}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			body := `{"commit_hash":"` + test.name + `","results":[` + test.result + `]}`
			server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body)))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestCreateRunRollsBackCompleteRequest(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	server := &Server{db: database}
	body := `{"commit_hash":"abc","results":[
		{"category":"cat","name":"good","min_ns":1,"avg_ns":2,"max_ns":3,"total_ns":2,"iterations":1,"sample_count":1},
		{"category":"cat","name":"bad","min_ns":1,"avg_ns":2,"max_ns":3,"total_ns":2,"iterations":1,"sample_count":1,"sample_data_version":1,"summary_version":2,"samples":[{"sample_index":0,"avg_ns":0}]}
	]}`
	recorder := httptest.NewRecorder()
	server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body)))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial run remains: %d", count)
	}
}

func TestCreateRunSerializesIdempotencyWithInsertion(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	server := &Server{db: database}
	body := `{"commit_hash":"abc","commit_hash_full":"abcdef","machine_id":"runner","zig_optimize":"ReleaseFast","results":[{"category":"cat","name":"bench","min_ns":1,"avg_ns":2,"max_ns":3,"total_ns":2,"iterations":1,"sample_count":1}]}`

	recorders := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, recorder := range recorders {
		wait.Add(1)
		go func(recorder *httptest.ResponseRecorder) {
			defer wait.Done()
			<-start
			server.handleCreateRun(recorder, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body)))
		}(recorder)
	}
	close(start)
	wait.Wait()

	statuses := map[int]int{}
	for _, recorder := range recorders {
		statuses[recorder.Code]++
	}
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("statuses = %v, want one 201 and one 200", statuses)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("run count = %d, want 1", count)
	}
}
