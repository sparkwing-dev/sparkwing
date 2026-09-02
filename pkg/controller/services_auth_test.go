package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestServices_RequiresABearer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	raw, _, err := st.CreateToken("runner", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		WithCachePodURL("http://cache.internal:8080").
		WithLogsURL("http://logs.internal:8081").
		Handler())
	t.Cleanup(srv.Close)

	anon, err := http.Get(srv.URL + "/api/v1/services")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anon.Body.Close() }()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anon.StatusCode)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/services", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authed.Body.Close() }()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("runner-token status = %d, want 200", authed.StatusCode)
	}
	var body controller.ServicesResponse
	if err := json.NewDecoder(authed.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CachePod != "http://cache.internal:8080" {
		t.Fatalf("cache_pod = %q", body.CachePod)
	}
}
