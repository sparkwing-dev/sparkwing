package nodehost

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func acquiredLayerEntry(t *testing.T, depth uint8) *source.Entry {
	t.Helper()
	repo, sha := sourcetest.Repository(t)
	directory := ".sparkwing"
	for range int(depth % 4) {
		directory = filepath.Join(directory, "nested")
	}
	if err := os.MkdirAll(filepath.Join(repo, directory), 0o700); err != nil {
		t.Fatal(err)
	}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + repo, LocalCheckout: repo, SHA: sha, PipelineDir: directory}, source.Options{Base: procenv.Base{Local: true, Captured: os.Environ()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	return entry
}

func FuzzArtifactLayerUsesExecutionWorkspace(f *testing.F) {
	f.Add(uint8(0), []byte("result"), "produced in checkout")
	f.Add(uint8(3), []byte{0, 255}, "")
	f.Fuzz(func(t *testing.T, depth uint8, path []byte, value string) {
		if len(path) > 64 || len(value) > 1024 {
			t.Skip()
		}
		input, adapter, backend := artifactPublicationFixture(t)
		input.Storage = StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}
		input.Entry = acquiredLayerEntry(t, depth)
		root := input.Entry.CheckoutDirectory()
		name := "output-" + hex.EncodeToString(path)
		writePublishedFile(t, root, name, value, 0o600)
		writePublishedFile(t, input.Entry.Directory, name, "SDK-directory decoy", 0o600)
		finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "artifacts"}, input, nodeproto.JobSpec{Artifacts: []string{name}})
		if err != nil || finish == nil {
			t.Fatalf("prepare: %v", err)
		}
		result := store.NodeResult{Outcome: store.NodeSuccess}
		if err := finish(t.Context(), input, &result); err != nil {
			t.Fatal(err)
		}
		adapter.Producer = func(context.Context, NodeInput, string) (blob.RecordKey, error) { return backend.upload.Key, nil }
		if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "artifacts"}, input, nodeproto.JobSpec{Consumes: []nodeproto.ArtifactInput{{Producer: backend.upload.Key.Scope.Job, Into: "received"}}}); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(root, "received", name))
		if err != nil || string(body) != value {
			t.Fatalf("execution workspace artifact: %q %v", body, err)
		}
		if _, err := os.Stat(filepath.Join(input.Entry.Directory, "received")); !os.IsNotExist(err) {
			t.Fatalf("restored into the SDK directory: %v", err)
		}
	})
}

func TestDepcacheLayerUsesExecutionWorkspace(t *testing.T) {
	input, adapter, backend, job := depcacheFixture(t)
	input.Entry = acquiredLayerEntry(t, 2)
	root := input.Entry.CheckoutDirectory()
	writePublishedFile(t, root, "lockfile", "dependencies-v1", 0o600)
	finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
	if err != nil || finish == nil {
		t.Fatalf("prepare cache: %v", err)
	}
	writePublishedFile(t, root, "vendor/module/file", "downloaded dependency", 0o600)
	result := store.NodeResult{Outcome: store.NodeSuccess}
	if err := finish(t.Context(), input, &result); err != nil || backend.commit == nil {
		t.Fatalf("save checkout cache: %v, committed=%v", err, backend.commit != nil)
	}
	if err := os.RemoveAll(filepath.Join(root, "vendor")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "vendor/module/file"))
	if err != nil || string(body) != "downloaded dependency" {
		t.Fatalf("restore checkout cache: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(input.Entry.Directory, "vendor")); !os.IsNotExist(err) {
		t.Fatalf("restored cache into SDK directory: %v", err)
	}
}
