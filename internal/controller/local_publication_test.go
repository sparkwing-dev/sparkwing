package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLocalPublicationSnapshotRequiresVerifiedPeer(t *testing.T) {
	st := newStoreForAuth(t)
	s := New(st, nil)
	raw, err := json.Marshal(runspec.SealedRequest{Version: 1, RunID: "local-history", Request: runspec.Request{Version: 1, Team: "default", Pipeline: "ci", Origin: runspec.OriginLocal, Mode: runspec.IntakeExecute}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('default','local-history',1,'ci','success',?,'request',1,'repo','host','execute',?,'local',1,2,'r30',30)`, raw, []byte(`[]`))
	services := s.HTTP()
	peer, _ := localControllerIdentity(t)
	listener := &localControllerListener{connections: make(chan net.Conn), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- services.ServeAPI(ctx, listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		local, remote := net.Pipe()
		select {
		case listener.connections <- localControllerConn{Conn: remote, peer: peer}:
			return local, nil
		case <-ctx.Done():
			_ = local.Close()
			_ = remote.Close()
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: transport}}
	state, err := client.ReadLocalPublication(t.Context(), options, "local-history")
	if err != nil || state.Run.ID != "local-history" || state.Run.FinishedAt == nil {
		t.Fatalf("local publication export: %+v %v", state, err)
	}
	property := func(value []byte) bool {
		request, err := http.NewRequestWithContext(t.Context(), "GET", "http://local/api/v1/runs/local-history/local-host/snapshot", nil)
		if err != nil {
			return false
		}
		request.Header.Set("Authorization", "Bearer forged-"+hex.EncodeToString(value))
		request.Header.Set("X-Local-UID", "1234")
		response, err := options.HTTP.Do(request)
		if err != nil {
			return false
		}
		return response.Body.Close() == nil && response.StatusCode == http.StatusUnauthorized
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	ordinary := httptest.NewRecorder()
	s.Handler().ServeHTTP(ordinary, httptest.NewRequest("GET", "/api/v1/runs/local-history/local-host/snapshot", nil))
	if ordinary.Code < 400 || strings.Contains(ordinary.Body.String(), "publication_records") {
		t.Fatalf("unverified request exported local state: %d %s", ordinary.Code, ordinary.Body.String())
	}
}
