package localcontroller

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeServicesSavesAndRestoresDeclaredCache(t *testing.T) {
	nativeCacheRoundTrip(t, "modules", "downloaded dependency", 7, 0, false)
}

func TestNativeServicesResolvesCacheFromPersistedJobEnvironment(t *testing.T) {
	nativeCacheRoundTrip(t, "go-modules", "downloaded module", 7, 0, true)
}

func FuzzNativeCachePublicationKeepsAwardScope(f *testing.F) {
	for field := uint8(0); field < 8; field++ {
		f.Add(field, uint8(1), []byte("modules"), "dependency")
	}
	f.Fuzz(func(t *testing.T, field, delta uint8, name []byte, value string) {
		if len(name) > 32 || len(value) > 1024 {
			t.Skip()
		}
		nativeCacheRoundTrip(t, "cache-"+hex.EncodeToString(name), value, field%8, delta, false)
	})
}

func nativeCacheRoundTrip(t *testing.T, name, value string, field, delta uint8, resolve bool) {
	t.Helper()
	cfg, state := nativeConfigFixture(t)
	state.snapshot.Run.Pipeline = "burst"
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	input, _ := nativeArtifactInput(t)
	backend := &nativeCacheBackend{}
	input.Storage.Bytes, input.Storage.Records = backend, backend
	directory := filepath.Join(input.Entry.Directory, "modules")
	cache := nodeproto.CacheSpec{Name: name, Path: "modules", KeyFiles: []string{"result.txt"}}
	job := nodeproto.JobSpec{ID: "job", Caches: []nodeproto.CacheSpec{cache}}
	if resolve {
		job.Caches[0].Resolver, job.Caches[0].Path = "go-modules", ""
		job.Env = map[string]nodeproto.EnvValue{"GOMODCACHE": {Value: directory}}
		body, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		input.SpecHash = "job-hash"
		state.node = store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Generation: input.Generation, SpecHash: input.SpecHash, Spec: body}
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		spec.Request.Request.Env = state.snapshot.Run.Request.Request.Env
		input.Accepted.Spec, err = json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := nodehost.NewNativeHost(services.Config); err != nil {
			t.Fatal(err)
		}
	}
	prepare := services.Config.Executor.PrepareLayer
	finish, err := prepare(t.Context(), nodehost.LayerSpec{Name: "caches"}, input, job)
	if err != nil || finish == nil {
		t.Fatalf("prepare declared cache: %v", err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "dependency"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	switch field {
	case 0:
		input.Accepted.RunID += "-foreign"
	case 1:
		input.NodeID += "-foreign"
	case 2:
		input.Generation += runspec.ClaimGeneration(delta) + 1
	case 3:
		input.Attempt += runspec.AttemptOrdinal(delta) + 1
	case 4, 5:
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		if field == 4 {
			spec.Request.Request.Pipeline += "-foreign"
		} else {
			spec.Request.Request.Git.Repository.Key += "-foreign"
		}
		input.Accepted.Spec, err = json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
	case 6:
		input.Storage.Authority.Ref.Scope.Ref += "-foreign"
	}
	if err := finish(t.Context(), input, &store.NodeResult{Outcome: store.NodeSuccess}); err != nil {
		t.Fatal(err)
	}
	if field < 7 {
		if backend.committed.Digest != "" || backend.body != nil {
			t.Fatal("foreign execution published under another award")
		}
		return
	}
	key := backend.committed.Key
	if backend.committed.Authority != input.Storage.Authority || backend.committed.Source != input.Storage.Authority.Upload.Value || key.Kind != blob.KindDepcache || key.Team != "default" || key.Scope.Pipeline != "" || key.Scope.Job != "" || key.Scope.Repo != input.Storage.Authority.Repo || key.Name != name || key.Scope.Key == "" {
		t.Fatalf("cache publication lost its awarded scope: %+v", backend.committed)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(t.Context(), nodehost.LayerSpec{Name: "caches"}, input, job); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(directory, "dependency"))
	if err != nil || string(body) != value || backend.lookup.Authority != input.Storage.Authority || backend.download.Authority != input.Storage.Authority || backend.download.Digest != backend.committed.Digest {
		t.Fatalf("native cache did not restore its authorized bytes: %q %v", body, err)
	}
}

type nativeCacheBackend struct {
	blob.Store
	blob.RecordStore
	body      []byte
	committed blob.CommitRecordRequest
	lookup    blob.DepcacheLookup
	download  blob.GetRequest
}

func (b *nativeCacheBackend) Put(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
	body, err := io.ReadAll(request.Body)
	b.body = body
	return blob.Receipt{Identity: request.Identity, Digest: request.Digest}, err
}

func (b *nativeCacheBackend) CommitRecord(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	b.committed = request
	return blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source}, nil
}

func (b *nativeCacheBackend) LookupDepcache(_ context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
	b.lookup = request
	if b.committed.Digest == "" {
		return blob.DepcacheHit{}, nil
	}
	return blob.DepcacheHit{Found: true, Record: blob.Record{Key: b.committed.Key, Digest: b.committed.Digest}}, nil
}

func (b *nativeCacheBackend) Get(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
	b.download = request
	return nativeCacheReader{bytes.NewReader(b.body)}, nil
}

type nativeCacheReader struct{ *bytes.Reader }

func (r nativeCacheReader) Verified() bool { return r.Len() == 0 }
func (nativeCacheReader) Close() error     { return nil }
