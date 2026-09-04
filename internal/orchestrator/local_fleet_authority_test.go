package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAllowFleetTokenPrefixesRejectsEveryOtherLocalCredential(t *testing.T) {
	allowedPrefix := "swr_12345678"
	reached := 0
	handler := allowFleetTokenPrefixes(map[string]struct{}{allowedPrefix: {}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, authorization string
		wantStatus          int
	}{
		{"allowed exact prefix", "Bearer " + allowedPrefix + "secret", http.StatusNoContent},
		{"another runner", "Bearer swr_87654321secret", http.StatusUnauthorized},
		{"same principal is irrelevant", "Bearer swr_00000000secret", http.StatusUnauthorized},
		{"short bearer", "Bearer swr_short", http.StatusUnauthorized},
		{"session cannot bypass", "Session local", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
			req.Header.Set("Authorization", tc.authorization)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tc.wantStatus)
			}
		})
	}
	if reached != 1 {
		t.Fatalf("downstream reached %d times, want exactly one", reached)
	}
	if len(allowedPrefix) != store.PrefixLen {
		t.Fatalf("test prefix length = %d, want %d", len(allowedPrefix), store.PrefixLen)
	}
}

func TestStartLocalFleetAuthorityRefusesAnOccupiedFixedEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	_, err = startLocalFleetAuthority(nil, "run-1", fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{Name: "local", MaxConcurrent: 1, Contribution: "50%,50%"},
	}, &Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "fleet coordinator listen "+address) {
		t.Fatalf("occupied endpoint error = %v", err)
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("failed start disturbed the existing endpoint: %v", err)
	}
	_ = conn.Close()
}

func TestStartLocalFleetAuthorityReleasesEndpointWhenInitializationFails(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = startLocalFleetAuthority(st, "run-1", fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{Name: "local", MaxConcurrent: 1, Contribution: "50%,50%"},
	}, &Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "reset fleet executor liveness") {
		t.Fatalf("initialization error = %v", err)
	}
	reused, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("failed initialization leaked its listener: %v", err)
	}
	_ = reused.Close()
}

func TestLocalFleetAuthorityCloseRevokesEphemeralAccessAndClosesSurfaces(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fixture := newFleetSourceFixture(t)
	authority, err := startLocalFleetAuthority(st, "run-close", fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{Name: "local", MaxConcurrent: 1, Contribution: "50%,50%"},
	}, &Options{
		Pipeline: "test", FleetSourceRoot: fixture.root, FleetSourceBundle: fixture.bundle,
		FleetSourceSHA: fixture.sha, FleetSourceManifestDigest: fixture.manifest, FleetSourceRepoURL: fixture.repoURL,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := authority.localToken
	sourceURL := authority.source.url
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+address+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("live authority health = %v, response %v", err, resp)
	}
	_ = resp.Body.Close()

	authority.Close()
	if _, err := st.LookupToken(raw, time.Now().UTC()); !errors.Is(err, store.ErrTokenRevoked) {
		t.Fatalf("ephemeral local credential after close = %v", err)
	}
	for _, endpoint := range []string{"http://" + address + "/api/v1/health", sourceURL + "/git/register"} {
		if response, requestErr := client.Get(endpoint); requestErr == nil {
			_ = response.Body.Close()
			t.Fatalf("closed authority surface remained reachable: %s", endpoint)
		}
	}
}

func TestLocalFleetAuthorityStopClaimsRejectsNewPrepareAndOfferOverAuthenticatedListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, _, err := st.ProvisionExecutor(context.Background(), "fleet-executor:helper", store.Executor{
		Name: "helper", Kind: "agent", Location: "local", Capabilities: []string{"helper-cap"},
		BasePriority: 50, PriorityCeiling: 100, MaxConcurrent: 1,
	}, []string{controller.ScopeNodesClaim, controller.ScopeRunsState}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	fixture := newFleetSourceFixture(t)
	authority, err := startLocalFleetAuthority(st, "run-stop", fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{Name: "local", MaxConcurrent: 1, Contribution: "50%,50%"},
		Executors: []fleet.Executor{{
			Name: "helper", Location: "local", Capabilities: []string{"helper-cap"},
			BasePriority: 50, PriorityCeiling: 100, MaxConcurrent: 1,
		}},
	}, &Options{
		Pipeline: "test", FleetSourceRoot: fixture.root, FleetSourceBundle: fixture.bundle,
		FleetSourceSHA: fixture.sha, FleetSourceManifestDigest: fixture.manifest, FleetSourceRepoURL: fixture.repoURL,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	remote := client.NewWithToken("http://"+address, nil, raw)
	if err := remote.HeartbeatExecutor(ctx, "helper", client.Headroom{Cores: 1}); err != nil {
		t.Fatalf("authenticated listener heartbeat: %v", err)
	}
	if preparation, err := remote.PrepareExecutorClaim(ctx, "helper"); err != nil || preparation != nil {
		t.Fatalf("prepare before stop = (%v, %v), want empty success", preparation, err)
	}

	authority.StopClaims()
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/api/v1/nodes/claim/prepare", `{"executor_name":"helper"}`},
		{"/api/v1/nodes/claim", `{"executor_name":"helper"}`},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+tc.path, bytes.NewBufferString(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("stopped claim request %s: %v", tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("stopped claim request %s = %d, want %d", tc.path, resp.StatusCode, http.StatusServiceUnavailable)
		}
	}
}

func TestCoordinatorProcessLossIsCancellation(t *testing.T) {
	if got := statusForRunError(fleet.ErrCoordinatorProcessGone); got != "cancelled" {
		t.Fatalf("coordinator-process loss status = %q", got)
	}
}
