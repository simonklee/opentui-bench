package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/joblease"
	"opentui-bench/internal/jsbench"
	"opentui-bench/internal/record"
)

func TestRecordRunSendsPrecisionFieldsToLegacyCompatibleServer(t *testing.T) {
	var request createRunRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/capabilities" {
			_, _ = fmt.Fprint(w, `{"javascript_runs":1}`)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":7,"result_ids":{"cat/bench":11}}`)
	}))
	defer server.Close()
	variance := 0.5
	rsd := int64(1000)
	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, ids, err := recorder.RecordRun(context.Background(), &record.ParsedRun{Meta: record.RunMetadata{
		BenchmarkKind: "js", BenchmarkSuite: jsbench.Suite, ProtocolVersion: jsbench.Protocol,
		BunVersion: jsbench.BunVersion, ZigVersion: jsbench.ZigVersion,
		ManifestHash: jsbench.ManifestDigest, ManifestJSON: `{"hash":"test"}`,
	}, Results: []record.ParsedResult{{
		Category: "cat", Name: "bench", AvgNs: 1, SampleCount: 2,
		SampleAvgVarianceNs2: &variance, SampleDataVersion: 1, SummaryVersion: 2,
		Samples: []db.ResultSample{{
			SampleIndex: 0, AvgNs: 1, InnerRSDPPM: &rsd,
			Batches: []db.ResultSampleBatch{{BatchIndex: 0, ElapsedNs: 10, Iterations: 10}},
		}, {SampleIndex: 2, AvgNs: 2}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if ids[db.BenchmarkKey{Category: "cat", Name: "bench"}] != 11 {
		t.Fatalf("legacy result IDs = %+v", ids)
	}
	got := request.Results[0]
	if request.BenchmarkKind != "js" || request.BunVersion != jsbench.BunVersion || request.ManifestHash != jsbench.ManifestDigest {
		t.Fatalf("run identity = %+v", request)
	}
	if got.SampleAvgVarianceNs2 == nil || *got.SampleAvgVarianceNs2 != 0.5 || got.SampleDataVersion != 1 || got.SummaryVersion != 2 ||
		len(got.Samples) != 2 || got.Samples[0].InnerRSDPPM == nil || len(got.Samples[0].Batches) != 1 {
		t.Fatalf("precision payload = %+v", got)
	}
}

func TestRecordJavaScriptRunRequiresServerCapability(t *testing.T) {
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, _, err := recorder.RecordRun(context.Background(), &record.ParsedRun{Meta: record.RunMetadata{BenchmarkKind: "js"}})
	if err == nil || !strings.Contains(err.Error(), "does not support JavaScript") {
		t.Fatalf("RecordRun error = %v, want missing capability error", err)
	}
	if posted {
		t.Fatal("JavaScript run was posted without server capability")
	}
}

func TestRecordRunRejectsAmbiguousLegacyResultKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":7,"result_ids":{"a/b/c":11}}`)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, _, err := recorder.RecordRun(context.Background(), &record.ParsedRun{Results: []record.ParsedResult{
		{Category: "a", Name: "b/c"},
		{Category: "a/b", Name: "c"},
	}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("RecordRun error = %v, want ambiguous legacy key error", err)
	}
}

func TestFinalizeArtifactsUsesAuthenticatedRunEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/7/artifacts/finalize" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL, APIKey: "secret"}
	if err := recorder.FinalizeArtifacts(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteJobMethodsCarryClaimToken(t *testing.T) {
	var update map[string]interface{}
	var claimedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/capabilities":
			_, _ = fmt.Fprintf(w, `{"job_lease_protocol":%d}`, joblease.Protocol)
		case r.Method == http.MethodPost && r.URL.Path == "/api/jobs/claim":
			if r.URL.Query().Get("benchmark_kind") != "zig" || r.URL.Query().Get("job_lease_protocol") != fmt.Sprint(joblease.Protocol) {
				t.Errorf("claim query = %q", r.URL.RawQuery)
			}
			if _, present := r.URL.Query()["javascript_runtimes"]; !present || r.URL.Query().Get("javascript_runtimes") != "" {
				t.Errorf("empty runtime capabilities were not advertised explicitly: %q", r.URL.RawQuery)
			}
			var request struct {
				ClaimToken string `json:"claim_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			claimedToken = request.ClaimToken
			_, _ = fmt.Fprint(w, `{"id":7,"status":"running"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/jobs/7":
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Error(err)
			}
			_, _ = fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	job, err := recorder.ClaimJob(context.Background(), "zig")
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != 7 || job.ClaimToken != claimedToken || joblease.ValidateToken(job.ClaimToken) != nil {
		t.Fatalf("claimed job = %+v", job)
	}
	payload := map[string]interface{}{"status": "failed", "error": "expected"}
	if err := recorder.UpdateJob(context.Background(), job.ID, job.ClaimToken, payload); err != nil {
		t.Fatal(err)
	}
	if _, leaked := payload["claim_token"]; leaked {
		t.Fatal("UpdateJob retained the claim token in caller-owned data")
	}
	if update["claim_token"] != claimedToken || update["status"] != "failed" {
		t.Fatalf("update payload = %+v", update)
	}
}

func TestRemoteClaimRetriesLostResponseWithSameToken(t *testing.T) {
	var attempts atomic.Int32
	var firstToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/capabilities" {
			_, _ = fmt.Fprintf(w, `{"job_lease_protocol":%d}`, joblease.Protocol)
			return
		}
		var request struct {
			ClaimToken string `json:"claim_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if attempts.Add(1) == 1 {
			firstToken = request.ClaimToken
			panic(http.ErrAbortHandler)
		}
		if request.ClaimToken != firstToken {
			t.Errorf("retry token = %q, want %q", request.ClaimToken, firstToken)
		}
		_, _ = fmt.Fprint(w, `{"id":7,"status":"running"}`)
	}))
	defer server.Close()

	job, err := (&RemoteRecorder{BaseURL: server.URL}).ClaimJob(context.Background(), "zig")
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || job == nil || job.ID != 7 || job.ClaimToken != firstToken {
		t.Fatalf("attempts/job = %d/%+v", attempts.Load(), job)
	}
}

func TestNewWorkerDoesNotClaimFromLegacyServer(t *testing.T) {
	claimRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/capabilities" {
			_, _ = fmt.Fprint(w, `{"javascript_runs":1}`)
			return
		}
		if r.URL.Path == "/api/jobs/claim" {
			claimRequested = true
			_, _ = fmt.Fprint(w, `{"id":7,"status":"running"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	job, err := recorder.ClaimJob(context.Background(), "zig")
	if err == nil || !strings.Contains(err.Error(), "does not support job lease protocol") {
		t.Fatalf("ClaimJob job/error = %+v/%v, want missing lease capability", job, err)
	}
	if claimRequested {
		t.Fatal("new worker sent a claim request to a legacy server")
	}
}

func TestRemoteUpdateJobRequiresClaimToken(t *testing.T) {
	recorder := &RemoteRecorder{BaseURL: "http://unused.invalid"}
	if err := recorder.UpdateJob(context.Background(), 7, "", map[string]interface{}{"status": "failed"}); err == nil {
		t.Fatal("UpdateJob accepted an empty claim token")
	}
}

func TestRemoteUpdateJobIdentifiesClaimLoss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "claim lost", http.StatusConflict)
	}))
	defer server.Close()

	err := (&RemoteRecorder{BaseURL: server.URL}).UpdateJob(context.Background(), 7, "stale", map[string]interface{}{"status": "completed", "run_id": 42})
	if !errors.Is(err, ErrJobClaimLost) {
		t.Fatalf("UpdateJob error = %v, want ErrJobClaimLost", err)
	}
}

func TestRemoteFailJobRetriesFailureBeforeCommit(t *testing.T) {
	var patches atomic.Int32
	var gets atomic.Int32
	var status atomic.Value
	status.Store("running")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			if patches.Add(1) == 1 {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
			status.Store("failed")
			_, _ = fmt.Fprint(w, `{}`)
		case http.MethodGet:
			gets.Add(1)
			_, _ = fmt.Fprintf(w, `{"status":%q}`, status.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := (&RemoteRecorder{BaseURL: server.URL}).FailJob(context.Background(), 7, "claim", "benchmark failed")
	if err != nil {
		t.Fatal(err)
	}
	if patches.Load() != 2 || gets.Load() != 1 || status.Load() != "failed" {
		t.Fatalf("patches/gets/status = %d/%d/%s, want 2/1/failed", patches.Load(), gets.Load(), status.Load())
	}
}

func TestRemoteCompleteJobVerifiesCommittedLostResponse(t *testing.T) {
	var patches atomic.Int32
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			patches.Add(1)
			panic(http.ErrAbortHandler)
		case http.MethodGet:
			gets.Add(1)
			_, _ = fmt.Fprint(w, `{"status":"completed","run_id":42}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := (&RemoteRecorder{BaseURL: server.URL}).CompleteJob(context.Background(), 7, "claim", 42)
	if err != nil {
		t.Fatal(err)
	}
	if patches.Load() != 1 || gets.Load() != 1 {
		t.Fatalf("patches/gets = %d/%d, want 1/1", patches.Load(), gets.Load())
	}
}

func TestRemoteCompleteJobDoesNotAcceptDifferentRun(t *testing.T) {
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches.Add(1)
			panic(http.ErrAbortHandler)
		}
		_, _ = fmt.Fprint(w, `{"status":"completed","run_id":99}`)
	}))
	defer server.Close()

	err := (&RemoteRecorder{BaseURL: server.URL}).CompleteJob(context.Background(), 7, "claim", 42)
	if !errors.Is(err, ErrJobClaimLost) {
		t.Fatalf("CompleteJob error = %v, want ErrJobClaimLost", err)
	}
	if patches.Load() != 1 {
		t.Fatalf("PATCH attempts = %d, want no retry after a different terminal result", patches.Load())
	}
}

func TestRemoteCompleteJobPersistentTransientFailureIsBounded(t *testing.T) {
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches.Add(1)
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := (&RemoteRecorder{BaseURL: server.URL}).CompleteJob(ctx, 7, "claim", 42)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CompleteJob error = %v, want persistent server error before deadline", err)
	}
	if patches.Load() != remoteJobMaxAttempts {
		t.Fatalf("PATCH attempts = %d, want %d", patches.Load(), remoteJobMaxAttempts)
	}
}

func TestRemoteCompleteJobRetriesInvalidVerificationBodies(t *testing.T) {
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			panic(http.ErrAbortHandler)
		}
		switch gets.Add(1) {
		case 1:
			_, _ = fmt.Fprint(w, `{"status":`)
		case 2:
			_, _ = fmt.Fprint(w, `{}`)
		default:
			_, _ = fmt.Fprint(w, `{"status":"completed","run_id":42}`)
		}
	}))
	defer server.Close()

	if err := (&RemoteRecorder{BaseURL: server.URL}).CompleteJob(context.Background(), 7, "claim", 42); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != remoteJobMaxAttempts {
		t.Fatalf("verification attempts = %d, want %d", gets.Load(), remoteJobMaxAttempts)
	}
}

func TestRecordRunRecoversAmbiguousShutdownResponseOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			cancel()
			panic(http.ErrAbortHandler)
		}
		_, _ = fmt.Fprint(w, `{"id":7}`)
	}))
	defer server.Close()

	runID, _, err := (&RemoteRecorder{BaseURL: server.URL}).RecordRun(ctx, &record.ParsedRun{})
	if err != nil {
		t.Fatal(err)
	}
	if runID != 7 || attempts.Load() != 2 {
		t.Fatalf("run ID/attempts = %d/%d, want 7/2", runID, attempts.Load())
	}
}

func TestRecordRunRecoversAmbiguousResponseWithHealthyContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		firstBody string
		abort     bool
	}{
		{name: "connection reset", abort: true},
		{name: "truncated success", firstBody: `{"id":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			var firstRequest string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if attempts.Add(1) == 1 {
					firstRequest = string(body)
					if test.abort {
						panic(http.ErrAbortHandler)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, test.firstBody)
					return
				}
				if string(body) != firstRequest {
					t.Errorf("recovery request changed:\nfirst: %s\nsecond: %s", firstRequest, body)
				}
				_, _ = fmt.Fprint(w, `{"id":7}`)
			}))
			defer server.Close()

			ctx := context.Background()
			runID, _, err := (&RemoteRecorder{BaseURL: server.URL}).RecordRun(ctx, &record.ParsedRun{})
			if err != nil {
				t.Fatal(err)
			}
			if ctx.Err() != nil || runID != 7 || attempts.Load() != 2 {
				t.Fatalf("context/run ID/attempts = %v/%d/%d, want healthy/7/2", ctx.Err(), runID, attempts.Load())
			}
		})
	}
}

func TestRecordRunPreservesRecoveryRejection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			cancel()
			panic(http.ErrAbortHandler)
		}
		http.Error(w, "invalid run", http.StatusBadRequest)
	}))
	defer server.Close()

	_, _, err := (&RemoteRecorder{BaseURL: server.URL}).RecordRun(ctx, &record.ParsedRun{})
	if err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "invalid run") {
		t.Fatalf("RecordRun error = %v, want definitive recovery rejection", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestRecordRunDoesNotRetryDefinitiveRejection(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "invalid run", http.StatusBadRequest)
	}))
	defer server.Close()

	_, _, err := (&RemoteRecorder{BaseURL: server.URL}).RecordRun(context.Background(), &record.ParsedRun{})
	if err == nil || !strings.Contains(err.Error(), "invalid run") {
		t.Fatalf("RecordRun error = %v, want definitive rejection", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestRemoteReleaseJobStopsWhenClaimIsLost(t *testing.T) {
	var patches atomic.Int32
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			if patches.Add(1) == 1 {
				http.Error(w, "temporary", http.StatusBadGateway)
				return
			}
			http.Error(w, "replacement lease owns job", http.StatusConflict)
		case http.MethodGet:
			gets.Add(1)
			_, _ = fmt.Fprint(w, `{"status":"running"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := (&RemoteRecorder{BaseURL: server.URL}).ReleaseJob(context.Background(), 7, "stale")
	if !errors.Is(err, ErrJobClaimLost) {
		t.Fatalf("ReleaseJob error = %v, want ErrJobClaimLost", err)
	}
	if patches.Load() != 2 || gets.Load() != 1 {
		t.Fatalf("patches/gets = %d/%d, want 2/1", patches.Load(), gets.Load())
	}
}

func TestRemoteRecordingOperationsHonorCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/capabilities" {
			_, _ = fmt.Fprint(w, `{"javascript_runs":1}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":1}`)
	}))
	defer server.Close()
	recorder := &RemoteRecorder{BaseURL: server.URL}

	for _, test := range []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "capability", run: func(ctx context.Context) error {
			_, _, err := recorder.RecordRun(ctx, &record.ParsedRun{Meta: record.RunMetadata{BenchmarkKind: "js"}})
			return err
		}},
		{name: "run", run: func(ctx context.Context) error {
			_, _, err := recorder.RecordRun(ctx, &record.ParsedRun{})
			return err
		}},
		{name: "artifact", run: func(ctx context.Context) error {
			return recorder.UploadArtifact(ctx, 1, 2, CollectedArtifact{Kind: "cpu.pprof", Data: []byte("profile")})
		}},
		{name: "finalize", run: func(ctx context.Context) error {
			return recorder.FinalizeArtifacts(ctx, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := test.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}
