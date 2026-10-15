package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type manifestVerdicts struct {
	storage.LogStore
	verdicts map[string]logseg.Completeness
}

func (m manifestVerdicts) NodeLogCompleteness(_ context.Context, _, nodeID string) (logseg.Completeness, bool, error) {
	c, ok := m.verdicts[nodeID]
	return c, ok, nil
}

type completenessTransport func(*http.Request) (*http.Response, error)

func (f completenessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNodeLogCompleteness_ReportsTheVerdictTheDashboardDraws(t *testing.T) {
	ctrl := client.New("http://controller", &http.Client{Transport: completenessTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runs/run-1/jobs" {
			t.Fatalf("unexpected node inventory request: %s %s", r.Method, r.URL)
		}
		body, err := json.Marshal([]store.Node{{NodeID: "sealed"}, {NodeID: "silent"}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})})
	logStore := manifestVerdicts{verdicts: map[string]logseg.Completeness{
		"sealed": {State: logseg.StateComplete, Lines: 1},
		"silent": {State: logseg.StateCutOff, Lines: 1, Message: "the log stopped before its writer finished"},
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		NodeLogCompletenessHandler(backend.NewClientBackend(ctrl, logStore)))
	get := func(node string, want int) logseg.Completeness {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/"+node+"/completeness", nil))
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d; %s", node, rec.Code, want, rec.Body.String())
		}
		var c logseg.Completeness
		if want == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	if c := get("sealed", http.StatusOK); c.State != logseg.StateComplete || c.Message != "" {
		t.Fatalf("sealed node = %+v", c)
	}
	if c := get("silent", http.StatusOK); c.State != logseg.StateCutOff || c.Message == "" {
		t.Fatalf("unsealed node = %+v", c)
	}
	get("nope", http.StatusNotFound)

	mux = http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		NodeLogCompletenessHandler(backend.NewClientBackend(ctrl, nil)))
	if c := get("silent", http.StatusOK); c.State != logseg.StateUnknown {
		t.Fatalf("store without manifests = %+v", c)
	}
}
