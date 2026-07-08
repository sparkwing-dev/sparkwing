package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// triggerSpy is a minimal controller stand-in. It records request lines
// and captures trigger POST bodies, and serves just enough of the
// status-follow surface (GetRun returns a terminal run, ListNodes
// returns empty) for a non-detach follow to render once and exit.
type triggerSpy struct {
	mu     sync.Mutex
	reqs   []string
	bodies [][]byte
}

func (s *triggerSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			s.mu.Lock()
			s.bodies = append(s.bodies, body)
			s.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.TriggerResponse{RunID: "run-test", Status: "pending"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runs/run-test":
			_ = json.NewEncoder(w).Encode(store.Run{ID: "run-test", Pipeline: "release", Status: "success", StartedAt: time.Now()})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	})
}

func (s *triggerSpy) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func writeTriggerProfiles(t *testing.T, controllerURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := "profiles:\n" +
		"  prod: { controller: { url: " + controllerURL + " } }\n" +
		"  laptop: { state: { type: sqlite } }\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestPipelineTrigger_MissingProfile(t *testing.T) {
	err := runPipelineTrigger([]string{"release"})
	if err == nil {
		t.Fatal("expected --profile-required error")
	}
	if !strings.Contains(err.Error(), "--profile NAME is required") {
		t.Errorf("message = %q", err.Error())
	}
	if code := exitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestPipelineTrigger_ProfileNotFound(t *testing.T) {
	writeTriggerProfiles(t, "https://api.example.dev")
	err := runPipelineTrigger([]string{"release", "--profile", "bogus"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "bogus" not found`) {
		t.Errorf("message = %q", err.Error())
	}
}

func TestPipelineTrigger_NoController(t *testing.T) {
	writeTriggerProfiles(t, "https://api.example.dev")
	err := runPipelineTrigger([]string{"release", "--profile", "laptop"})
	if err == nil {
		t.Fatal("expected no-controller error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `profile "laptop" has no controller`) {
		t.Errorf("message should name the controller-less profile: %q", msg)
	}
	if !strings.Contains(msg, "sparkwing run --profile laptop") {
		t.Errorf("message should point at the local-run alternative: %q", msg)
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestPipelineTrigger_DetachFiresTriggerOnly(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	out := captureStdout(t, func() {
		if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach", "--version", "v1.2.3"}); err != nil {
			t.Errorf("trigger: %v", err)
		}
	})

	reqs := spy.requests()
	sawTrigger := false
	for _, r := range reqs {
		switch {
		case r == "POST /api/v1/triggers":
			sawTrigger = true
		case r == "GET /api/v1/services":
		case strings.HasPrefix(r, "GET /api/v1/runs"):
			t.Fatalf("detach should not follow the run; got %v", reqs)
		}
	}
	if !sawTrigger {
		t.Fatalf("expected POST /api/v1/triggers; got %v", reqs)
	}
	if strings.TrimSpace(out) != "run-test" {
		t.Errorf("detach stdout = %q, want run id", out)
	}
	if len(spy.bodies) != 1 {
		t.Fatalf("expected 1 trigger body, got %d", len(spy.bodies))
	}
	var req client.TriggerRequest
	if err := json.Unmarshal(spy.bodies[0], &req); err != nil {
		t.Fatalf("decode trigger body: %v", err)
	}
	if req.Pipeline != "release" {
		t.Errorf("pipeline = %q, want release", req.Pipeline)
	}
	if req.Args["version"] != "v1.2.3" {
		t.Errorf("args = %v, want version=v1.2.3", req.Args)
	}
	if !strings.HasPrefix(req.Trigger.Source, "pipeline-trigger") {
		t.Errorf("trigger source = %q, want pipeline-trigger prefix", req.Trigger.Source)
	}
}

func TestPipelineTrigger_DefaultFollows(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	_ = captureStdout(t, func() {
		if err := runPipelineTrigger([]string{"release", "--profile", "prod"}); err != nil {
			t.Errorf("trigger: %v", err)
		}
	})

	reqs := spy.requests()
	sawTrigger, sawFollow := false, false
	for _, r := range reqs {
		switch r {
		case "POST /api/v1/triggers":
			sawTrigger = true
		case "GET /api/v1/runs/run-test":
			sawFollow = true
		}
	}
	if !sawTrigger {
		t.Fatalf("expected POST /api/v1/triggers; got %v", reqs)
	}
	if !sawFollow {
		t.Fatalf("non-detach should follow the run (GET /api/v1/runs/run-test); got %v", reqs)
	}
}
