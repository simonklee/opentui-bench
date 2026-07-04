package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opentui-bench/internal/record"
)

func TestRecordRunRejectsAmbiguousLegacyResultKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":7,"result_ids":{"a/b/c":11}}`)
	}))
	defer server.Close()

	recorder := &RemoteRecorder{BaseURL: server.URL}
	_, _, err := recorder.RecordRun(&record.ParsedRun{Results: []record.ParsedResult{
		{Category: "a", Name: "b/c"},
		{Category: "a/b", Name: "c"},
	}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("RecordRun error = %v, want ambiguous legacy key error", err)
	}
}
