package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
		"  prod: { controller: " + controllerURL + " }\n" +
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

	// --detach: exactly the trigger POST, no follow GETs.
	reqs := spy.requests()
	if len(reqs) != 1 || reqs[0] != "POST /api/v1/triggers" {
		t.Fatalf("detach should fire only the trigger POST; got %v", reqs)
	}
	// stdout is the run id alone, machine-parseable.
	if strings.TrimSpace(out) != "run-test" {
		t.Errorf("detach stdout = %q, want run id", out)
	}
	// Pass-through args reached the payload.
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
	if len(reqs) == 0 || reqs[0] != "POST /api/v1/triggers" {
		t.Fatalf("first request should be the trigger POST; got %v", reqs)
	}
	// Without --detach the verb follows: it polls the run after the POST.
	sawFollow := false
	for _, r := range reqs[1:] {
		if r == "GET /api/v1/runs/run-test" {
			sawFollow = true
		}
	}
	if !sawFollow {
		t.Fatalf("non-detach should follow the run (GET /api/v1/runs/run-test); got %v", reqs)
	}
}

// TestPipelineTrigger_ParityWithRunSwProfile asserts the new verb POSTs a
// byte-identical trigger payload to today's `run --sw-profile` path,
// modulo the trigger_source tag.
func TestPipelineTrigger_ParityWithRunSwProfile(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	// Legacy path (dispatchRemote, called directly to avoid the
	// compile/discovery dispatchRun does first).
	_ = captureStdout(t, func() {
		if err := dispatchRemote("release", runFlags{on: "prod"}, []string{"--version", "v1.2.3"}); err != nil {
			t.Errorf("dispatchRemote: %v", err)
		}
	})
	// New verb, detached so we compare only the POST.
	_ = captureStdout(t, func() {
		if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach", "--version", "v1.2.3"}); err != nil {
			t.Errorf("pipeline trigger: %v", err)
		}
	})

	if len(spy.bodies) != 2 {
		t.Fatalf("expected 2 trigger bodies (legacy + new), got %d", len(spy.bodies))
	}
	var legacy, fresh client.TriggerRequest
	if err := json.Unmarshal(spy.bodies[0], &legacy); err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if err := json.Unmarshal(spy.bodies[1], &fresh); err != nil {
		t.Fatalf("decode new: %v", err)
	}
	// trigger_source intentionally differs; everything else must match.
	if legacy.Trigger.Source == fresh.Trigger.Source {
		t.Errorf("trigger sources should differ; both %q", legacy.Trigger.Source)
	}
	legacy.Trigger.Source = ""
	fresh.Trigger.Source = ""
	if !reflect.DeepEqual(legacy, fresh) {
		t.Errorf("payloads differ (modulo source):\nlegacy: %+v\nnew:    %+v", legacy, fresh)
	}
}
