package isolationprobe

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type controllerFake struct {
	mu       sync.Mutex
	requests []string
	auth     []string
	status   map[string][]int
}

func (c *controllerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r.Method+" "+r.URL.Path)
	c.auth = append(c.auth, r.Header.Get("Authorization"))
	code := http.StatusUnauthorized
	if queue := c.status[r.URL.Path]; len(queue) > 0 {
		code, c.status[r.URL.Path] = queue[0], queue[1:]
	}
	w.WriteHeader(code)
}

func TestRouteProbesSendNoCredentialAndPassOnlyRefusals(t *testing.T) {
	fake := &controllerFake{status: map[string][]int{"/api/v1/runs/run-own/jobs/output-sibling/output": {http.StatusConflict, http.StatusUnauthorized}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	waits := 0
	wait := func(context.Context) error { waits++; return nil }
	results := newNetwork().routeProbes(t.Context(), ContextJob, Plan{ControllerURL: srv.URL + "/", PeerRuns: []string{"run-peer"}}, "run-own", wait)
	if len(results) != 4 {
		t.Fatalf("route probes = %d, want 4", len(results))
	}
	for _, r := range results {
		if r.Verdict != Pass || r.Outcome != "http-401" {
			t.Errorf("%s = %+v, want a refused pass", r.Probe, r)
		}
	}
	if waits != 1 {
		t.Fatalf("waited %d times for the unfinished sibling, want 1", waits)
	}
	want := []string{"GET /api/v1/tokens", "POST /api/v1/runs/run-own/source-credential", "GET /api/v1/runs/run-own/jobs/output-sibling/output", "GET /api/v1/runs/run-own/jobs/output-sibling/output", "GET /api/v1/runs/run-peer/jobs/probe/output"}
	if strings.Join(fake.requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", fake.requests, want)
	}
	for _, a := range fake.auth {
		if a != "" {
			t.Fatalf("a route probe carried a credential: %q", a)
		}
	}
}

func TestRouteProbesFailAnAnswerAndAnUnsettledOutput(t *testing.T) {
	fake := &controllerFake{status: map[string][]int{
		"/api/v1/tokens": {http.StatusOK},
		"/api/v1/runs/run-own/jobs/output-sibling/output": {http.StatusConflict, http.StatusConflict},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	gaveUp := func(context.Context) error { return context.DeadlineExceeded }
	byProbe := map[string]Result{}
	for _, r := range newNetwork().routeProbes(t.Context(), ContextJob, Plan{ControllerURL: srv.URL}, "run-own", gaveUp) {
		byProbe[r.Probe] = r
	}
	if r := byProbe["anonymous token list"]; r.Verdict != Breach {
		t.Errorf("answered token list = %+v, want breach", r)
	}
	if r := byProbe["anonymous own-run sibling output"]; r.Verdict != Inconclusive || r.Outcome != "http-409" {
		t.Errorf("unfinished sibling = %+v, want inconclusive", r)
	}
}

func TestRouteProbesNeedTheRunsOwnID(t *testing.T) {
	results := newNetwork().routeProbes(t.Context(), ContextJob, Plan{ControllerURL: "https://staging.sparkwing.dev"}, "", nil)
	if len(results) != 1 || results[0].Verdict != NotProbed {
		t.Fatalf("route probes without a run = %+v, want one not-probed result", results)
	}
}

func TestCredentialProbesReportPresenceNeverTheNameOrValue(t *testing.T) {
	absent := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	clean := credentialProbes(ContextJob, []string{"PATH=/usr/bin", "HOME=/tmp", "DOCKER_HOST=unix:///tmp/docker.sock"}, "/tmp", absent)
	for _, r := range clean {
		if r.Verdict != Pass {
			t.Errorf("clean job: %+v, want pass", r)
		}
	}
	leaky := credentialProbes(ContextJob, []string{"AWS_SECRET_ACCESS_KEY=AKIAvalue", "AWS_WEB_IDENTITY_TOKEN_FILE=/var/run/irsa/token"}, "/tmp",
		func(path string) ([]byte, error) {
			if path == "/var/run/irsa/token" {
				return []byte("irsa-token-value"), nil
			}
			return nil, fs.ErrPermission
		})
	verdicts := map[string]Verdict{}
	for _, r := range leaky {
		verdicts[r.Probe] = r.Verdict
		for _, value := range []string{"AKIAvalue", "AWS_SECRET_ACCESS_KEY", "irsa-token-value"} {
			if strings.Contains(r.Probe+r.Outcome, value) {
				t.Errorf("%s leaks %q", r.Probe, value)
			}
		}
	}
	if verdicts["credential-named environment variable"] != Breach || verdicts["credential file token"] != Breach || verdicts["credential file .netrc"] != Pass {
		t.Fatalf("leaky job verdicts = %v", verdicts)
	}
	broken := credentialProbes(ContextJob, nil, "", func(string) ([]byte, error) { return nil, errors.New("I/O error") })
	if broken[1].Verdict != Inconclusive {
		t.Fatalf("unreadable token = %+v, want inconclusive", broken[1])
	}
}
