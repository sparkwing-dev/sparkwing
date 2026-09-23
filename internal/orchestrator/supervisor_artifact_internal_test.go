package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A pooled agent serves many runs from one process, so the store a node's
// artifacts go to carries that node's grant, not one from the environment.
func TestSupervisorArtifactsGoToTheNodesCacheWithItsGrant(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "")
	t.Setenv(DevEnvDisableEnv, "1")
	var bearer atomic.Value
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer cache.Close()

	store, err := supervisorArtifactStore(context.Background(), cache.URL, "swcg_node-grant")
	if err != nil || store == nil {
		t.Fatalf("store = %v, %v; want the node's cache", store, err)
	}
	if err := store.Put(context.Background(), "k", strings.NewReader("artifact")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := bearer.Load(); got != "Bearer swcg_node-grant" {
		t.Fatalf("the artifact write carried %v, want the node's grant", got)
	}

	for name, tc := range map[string]struct{ url, grant string }{
		"no grant":       {cache.URL, ""},
		"no cache named": {"", "swcg_node-grant"},
	} {
		if store, err := supervisorArtifactStore(context.Background(), tc.url, tc.grant); err != nil || store != nil {
			t.Errorf("%s: store = %v, %v; want none from an empty environment", name, store, err)
		}
	}
}
