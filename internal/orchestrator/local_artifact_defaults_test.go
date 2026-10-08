package orchestrator

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func TestLocalArtifactDefaultsReachNode(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "local-only"}[localOnly], func(t *testing.T) {
			neutralizeEnv(t)
			paths := Paths{Root: t.TempDir()}
			t.Setenv("SPARKWING_HOME", paths.Root)
			t.Setenv("SPARKWING_CONFIG", paths.Root+"/config.yaml")
			t.Setenv(ArtifactStoreEnvVar, "")
			if localOnly {
				t.Setenv("SPARKWING_LOCAL_ONLY", "1")
			} else {
				t.Setenv("SPARKWING_LOCAL_ONLY", "")
			}
			opts := Options{LocalOnly: localOnly, DefaultStateDB: paths.StateDB()}
			if err := applyProfileBackendsWithMirror(t.Context(), &opts, nil, paths, true); err != nil {
				t.Fatal(err)
			}
			if opts.ArtifactStore == nil {
				t.Fatal("local dispatcher has no artifact store")
			}
			if err := opts.ArtifactStore.Put(t.Context(), "probe", bytes.NewBufferString("exact artifact bytes")); err != nil {
				t.Fatal(err)
			}
			_, nodeStore, _, err := coordinatedChildSurfaces(t.Context(), "fixture", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if nodeStore == nil {
				t.Fatal("local node has no artifact store")
			}
			reader, err := nodeStore.Get(t.Context(), "probe")
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "exact artifact bytes" {
				t.Fatalf("artifact = %q", got)
			}
		})
	}
}

func TestLocalOnlyArtifactSetupNeverContactsController(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	paths := Paths{Root: t.TempDir()}
	prof := &profile.Profile{Name: "remote", Controller: &profile.ControllerSpec{URL: server.URL, Token: "test-token"}}
	opts := Options{LocalOnly: true, DefaultStateDB: paths.StateDB()}
	if err := applyProfileBackendsWithMirror(t.Context(), &opts, prof, paths, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("local-only backend setup sent %d controller requests", requests.Load())
	}
	if opts.ArtifactStore == nil {
		t.Fatal("local-only run has no artifact cache")
	}
}
