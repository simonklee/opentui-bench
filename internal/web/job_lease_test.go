package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"opentui-bench/internal/db"
	"opentui-bench/internal/joblease"
)

type claimedJobResponse struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	ClaimToken string `json:"-"`
}

func leaseClaimRequest(target string) *http.Request {
	token, err := joblease.NewToken()
	if err != nil {
		panic(err)
	}
	return leaseClaimRequestWithToken(target, token)
}

func leaseClaimRequestWithToken(target, token string) *http.Request {
	body := strings.NewReader(fmt.Sprintf(`{"claim_token":%q}`, token))
	req := httptest.NewRequest(http.MethodPost, target, body)
	query := req.URL.Query()
	query.Set(joblease.QueryParameter, strconv.Itoa(joblease.Protocol))
	req.URL.RawQuery = query.Encode()
	return req
}

func claimJobForTest(t *testing.T, server *Server) claimedJobResponse {
	t.Helper()
	token, err := joblease.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleClaimJob(recorder, leaseClaimRequestWithToken("/api/jobs/claim", token))
	if recorder.Code != http.StatusOK {
		t.Fatalf("claim status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "claim_token") || strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("claim response exposed raw token: %s", recorder.Body.String())
	}
	claimed := claimedJobResponse{ClaimToken: token}
	if err := json.NewDecoder(recorder.Body).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "running" || len(claimed.ClaimToken) != 64 {
		t.Fatalf("claim response = %+v", claimed)
	}
	return claimed
}

func TestRepeatedClaimTokenReturnsSameRunningJob(t *testing.T) {
	server, database := newAPIStorageServer(t)
	firstID, err := database.InsertJob(&db.Job{Status: "pending", Kind: "benchmark", Branch: "first", Samples: 3, Profile: "cpu", CreatedAt: "2026-08-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := database.InsertJob(&db.Job{Status: "pending", Kind: "benchmark", Branch: "second", Samples: 3, Profile: "cpu", CreatedAt: "2026-08-02T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := joblease.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	claim := func() claimedJobResponse {
		recorder := httptest.NewRecorder()
		server.handleClaimJob(recorder, leaseClaimRequestWithToken("/api/jobs/claim", token))
		if recorder.Code != http.StatusOK {
			t.Fatalf("claim status/body = %d: %s", recorder.Code, recorder.Body.String())
		}
		var response claimedJobResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first, repeated := claim(), claim()
	if first.ID != firstID || repeated.ID != firstID {
		t.Fatalf("claim IDs = %d, %d, want repeated %d", first.ID, repeated.ID, firstID)
	}
	second, err := database.GetJob(secondID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "pending" {
		t.Fatalf("repeated claim consumed second job: %+v", second)
	}
}

func TestClaimRejectsInvalidTokenWithoutConsumingJob(t *testing.T) {
	server, database := newAPIStorageServer(t)
	jobID, err := database.InsertJob(&db.Job{Status: "pending", Kind: "benchmark", Branch: "main", Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleClaimJob(recorder, leaseClaimRequestWithToken("/api/jobs/claim", "not-a-token"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid token status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	job, err := database.GetJob(jobID)
	if err != nil || job.Status != "pending" {
		t.Fatalf("invalid token changed job: %+v, err=%v", job, err)
	}
}

func TestOldWorkerCannotClaimFromLeaseServer(t *testing.T) {
	server, database := newAPIStorageServer(t)
	jobID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.handleClaimJob(recorder, httptest.NewRequest(http.MethodPost, "/api/jobs/claim", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("claim status/body = %d: %s", recorder.Code, recorder.Body.String())
	}
	job, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.ClaimToken != "" {
		t.Fatalf("old worker claimed job: %+v", job)
	}
}

func patchJobForTest(t *testing.T, server *Server, jobID int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.handleUpdateJob(recorder, httptest.NewRequest(http.MethodPatch,
		fmt.Sprintf("/api/jobs/%d", jobID), strings.NewReader(body)), jobID)
	return recorder
}

func TestJobAPIRequiresActiveClaimTokenAndKeepsItPrivate(t *testing.T) {
	server, database := newAPIStorageServer(t)
	jobID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	first := claimJobForTest(t, server)
	if first.ID != jobID {
		t.Fatalf("claimed job %d, want %d", first.ID, jobID)
	}

	list := httptest.NewRecorder()
	server.handleListJobs(list, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "claim_token") || strings.Contains(list.Body.String(), first.ClaimToken) {
		t.Fatalf("list exposed claim token: status=%d body=%s", list.Code, list.Body.String())
	}
	get := httptest.NewRecorder()
	server.handleGetJob(get, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/jobs/%d", jobID), nil), jobID)
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "claim_token") || strings.Contains(get.Body.String(), first.ClaimToken) {
		t.Fatalf("get exposed claim token: status=%d body=%s", get.Code, get.Body.String())
	}

	missing := patchJobForTest(t, server, jobID, `{"status":"failed"}`)
	if missing.Code != http.StatusConflict {
		t.Fatalf("missing token status/body = %d: %s", missing.Code, missing.Body.String())
	}
	wrong := patchJobForTest(t, server, jobID, `{"claim_token":"wrong","status":"failed","error":"stale"}`)
	if wrong.Code != http.StatusConflict {
		t.Fatalf("wrong token status/body = %d: %s", wrong.Code, wrong.Body.String())
	}

	commit := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"running","commit_hash":"abcdef"}`, first.ClaimToken))
	if commit.Code != http.StatusOK {
		t.Fatalf("commit update status/body = %d: %s", commit.Code, commit.Body.String())
	}
	release := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"pending"}`, first.ClaimToken))
	if release.Code != http.StatusOK {
		t.Fatalf("release status/body = %d: %s", release.Code, release.Body.String())
	}
	second := claimJobForTest(t, server)
	if second.ClaimToken == first.ClaimToken {
		t.Fatal("reclaim reused claim token")
	}

	stale := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"failed","error":"stale"}`, first.ClaimToken))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale token status/body = %d: %s", stale.Code, stale.Body.String())
	}
	fail := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"failed","error":"expected"}`, second.ClaimToken))
	if fail.Code != http.StatusOK {
		t.Fatalf("active failure status/body = %d: %s", fail.Code, fail.Body.String())
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || stored.Error != "expected" || stored.ClaimToken != "" {
		t.Fatalf("failed job = %+v", stored)
	}
}

func TestLegacyWorkerCanCompleteButNotMutateAfterReclaim(t *testing.T) {
	server, database := newAPIStorageServer(t)
	legacyID, err := database.InsertJob(&db.Job{
		Status: "running", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET started_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), legacyID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET legacy_tokenless = 1 WHERE id = ?`, legacyID); err != nil {
		t.Fatal(err)
	}
	commit := patchJobForTest(t, server, legacyID, `{"status":"running","commit_hash":"wanted"}`)
	if commit.Code != http.StatusOK {
		t.Fatalf("legacy commit status/body = %d: %s", commit.Code, commit.Body.String())
	}
	runID, err := database.InsertRun(&db.Run{CommitHash: "wanted", CommitHashFull: "wanted", RunDate: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	complete := patchJobForTest(t, server, legacyID, fmt.Sprintf(`{"status":"completed","run_id":%d}`, runID))
	if complete.Code != http.StatusOK {
		t.Fatalf("legacy completion status/body = %d: %s", complete.Code, complete.Body.String())
	}

	reclaimedID, err := database.InsertJob(&db.Job{
		Status: "running", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET started_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-db.JobLeaseDuration-time.Hour).Format(time.RFC3339), reclaimedID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE jobs SET legacy_tokenless = 1 WHERE id = ?`, reclaimedID); err != nil {
		t.Fatal(err)
	}
	claimed := claimJobForTest(t, server)
	if claimed.ID != reclaimedID {
		t.Fatalf("reclaimed job ID = %d, want %d", claimed.ID, reclaimedID)
	}
	stale := patchJobForTest(t, server, reclaimedID, `{"status":"failed","error":"old worker"}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("tokenless post-reclaim status/body = %d: %s", stale.Code, stale.Body.String())
	}
}

func TestJobAPIRejectsCompletionForWrongCommit(t *testing.T) {
	server, database := newAPIStorageServer(t)
	jobID, err := database.InsertJob(&db.Job{
		Status: "pending", Kind: "benchmark", Branch: "main", RepoURL: "origin",
		Samples: 3, Profile: "cpu", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed := claimJobForTest(t, server)
	commit := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"running","commit_hash":"wanted"}`, claimed.ClaimToken))
	if commit.Code != http.StatusOK {
		t.Fatalf("commit update status/body = %d: %s", commit.Code, commit.Body.String())
	}
	runID, err := database.InsertRun(&db.Run{
		CommitHash: "other", CommitHashFull: "other", RunDate: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	complete := patchJobForTest(t, server, jobID,
		fmt.Sprintf(`{"claim_token":%q,"status":"completed","run_id":%d}`, claimed.ClaimToken, runID))
	if complete.Code != http.StatusBadRequest {
		t.Fatalf("wrong-commit completion status/body = %d: %s", complete.Code, complete.Body.String())
	}
	stored, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || stored.RunID != nil {
		t.Fatalf("wrong-commit run completed job: %+v", stored)
	}
}
