package orchestrator

import (
	"bytes"
	"io"
	"testing"
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
			if err := resolveLocalBackends(&opts, paths, false); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opts.State.Close() })
			if opts.ArtifactStore == nil {
				t.Fatal("local dispatcher has no artifact store")
			}
			if err := opts.ArtifactStore.Put(t.Context(), "probe", bytes.NewBufferString("exact artifact bytes")); err != nil {
				t.Fatal(err)
			}
			_, nodeStore, err := coordinatedChildSurfaces(t.Context(), "fixture", "fixture")
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
