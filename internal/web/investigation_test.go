package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"opentui-bench/internal/db"
)

func TestInvestigationWritesRequireAuth(t *testing.T) {
	server := &Server{apiKey: "test-key"}
	for _, path := range []string{
		"/api/investigations", "/api/investigations/1/candidates",
		"/api/investigations/1/attempts", "/api/investigations/1/record",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			if path == "/api/investigations" {
				server.handleInvestigationsRoute(response, request)
			} else {
				server.routeInvestigationsAPI(response, request)
			}
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous request returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestInvestigationPairRetryAndSampleBounds(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{db: database, apiKey: "test-key"}
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-key")
		if path == "/api/investigations" {
			server.handleInvestigationsRoute(response, request)
		} else {
			server.routeInvestigationsAPI(response, request)
		}
		return response
	}
	for _, samples := range []int64{-1, int64(db.MaxInvestigationSamples + 1), 9223372036854775807} {
		body := fmt.Sprintf(`{"category":"cat","name":"bench","baseline_commit":"base","target_commit":"target","samples":%d}`, samples)
		response := post("/api/investigations", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("samples=%d: status=%d body=%s", samples, response.Code, response.Body.String())
		}
	}
	created := post("/api/investigations", `{"category":"cat","name":"bench","baseline_commit":"base","target_commit":"target","attempt_key":"initial"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		Investigation investigationResponse `json:"investigation"`
		Attempt       attemptResponse       `json:"attempt"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := database.FailAttempt(payload.Attempt.AttemptKey, "capture failed"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/investigations/%d/attempts", payload.Investigation.ID)
	body := `{"attempt_key":"fresh-pair","profile":"none"}`
	fresh := post(path, body)
	if fresh.Code != http.StatusCreated {
		t.Fatalf("fresh pair: %d %s", fresh.Code, fresh.Body.String())
	}
	var first, retried struct {
		Attempt attemptResponse `json:"attempt"`
		Job     jobResponse     `json:"job"`
	}
	if err := json.Unmarshal(fresh.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	retry := post(path, body)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retried); err != nil {
		t.Fatal(err)
	}
	if first.Attempt.ID == payload.Attempt.ID || first.Attempt.ID != retried.Attempt.ID || first.Job.ID != retried.Job.ID {
		t.Fatalf("attempt identities: original=%d fresh=%+v retry=%+v", payload.Attempt.ID, first, retried)
	}
	for _, endpoint := range []string{path, fmt.Sprintf("/api/investigations/%d/candidates", payload.Investigation.ID)} {
		body := fmt.Sprintf(`{"attempt_key":"over-budget","samples":%d}`, db.MaxInvestigationSamples+1)
		if strings.HasSuffix(endpoint, "/candidates") {
			body = fmt.Sprintf(`{"attempt_key":"over-budget","commit_hash":"fix","samples":%d}`, db.MaxInvestigationSamples+1)
		}
		if response := post(endpoint, body); response.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted oversized sample count: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
}

func TestCreateInvestigationReusesIdentityAndIsolatesHistory(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runID, err := database.InsertRun(&db.Run{
		CommitHash: "target", CommitHashFull: "targetfull", Branch: "main",
		RunDate: "2026-09-07T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultID, err := database.InsertResult(&db.Result{
		RunID: runID, Category: "Sixel", Name: "160x240 flat",
		MinNs: 10, AvgNs: 10, MaxNs: 10, P50Ns: 10, P95Ns: 10, P99Ns: 10,
		TotalNs: 10, Iterations: 1, SampleCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	refID, err := database.InsertRun(&db.Run{
		CommitHash: "base", CommitHashFull: "basefull", Branch: "main",
		RunDate: "2026-08-01T00:00:00Z", MachineID: "runner", ZigOptimize: "ReleaseFast",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{db: database}

	create := httptest.NewRecorder()
	body := `{"trigger_result_id":` + strconv.FormatInt(resultID, 10) + `,"statistical_reference_run_id":` + strconv.FormatInt(refID, 10) + `,"attempt_key":"pair-1"}`
	server.handleCreateInvestigation(create, httptest.NewRequest(http.MethodPost, "/api/investigations", strings.NewReader(body)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	var created struct {
		Investigation struct {
			ID int64 `json:"id"`
		} `json:"investigation"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Investigation.ID == 0 {
		t.Fatalf("create payload = %s", create.Body.String())
	}

	retry := httptest.NewRecorder()
	retryBody := `{"trigger_result_id":` + strconv.FormatInt(resultID, 10) + `,"statistical_reference_run_id":` + strconv.FormatInt(refID, 10) + `,"attempt_key":"pair-2"}`
	server.handleCreateInvestigation(retry, httptest.NewRequest(http.MethodPost, "/api/investigations", strings.NewReader(retryBody)))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", retry.Code, retry.Body.String())
	}
	var reused struct {
		Investigation struct {
			ID int64 `json:"id"`
		} `json:"investigation"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Created || reused.Investigation.ID != created.Investigation.ID {
		t.Fatalf("retry payload = %s", retry.Body.String())
	}

	evidence := httptest.NewRecorder()
	server.handleInvestigationEvidence(evidence, httptest.NewRequest(http.MethodGet, "/api/investigations/1/evidence", nil), created.Investigation.ID)
	if evidence.Code != http.StatusOK {
		t.Fatalf("evidence status = %d: %s", evidence.Code, evidence.Body.String())
	}
	if !strings.Contains(evidence.Body.String(), `"uncalibrated_regression_score":true`) {
		t.Fatalf("evidence = %s", evidence.Body.String())
	}

	latest := httptest.NewRecorder()
	server.handleLatestCommit(latest, httptest.NewRequest(http.MethodGet, "/api/latest-commit?branch=main", nil))
	if !strings.Contains(latest.Body.String(), `"commit_hash":"target"`) {
		t.Fatalf("latest = %s", latest.Body.String())
	}
}
