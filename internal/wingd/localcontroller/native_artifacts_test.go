package localcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeServicesPublishesAndConsumesArtifacts(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "consume"}[consume], func(t *testing.T) {
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
			prepare := services.Config.Executor.PrepareLayer
			if prepare == nil {
				t.Fatal("production native host has no artifact adapter")
			}
			input, backend := nativeArtifactInput(t)
			job := nodeproto.JobSpec{ID: "job", Artifacts: []string{"result.txt"}}
			if consume {
				key := blob.ResultRecordKey(runspec.AttemptIdentity{Team: "default", RunID: "run", NodeID: "producer", Generation: 4, Attempt: 2}, input.Storage.Authority.Repo, input.Storage.Authority.Ref.Scope, "burst", blob.KindManifest)
				state.node = store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "producer"}, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: 4, Attempt: 2, ManifestRecord: &key}
				job.Artifacts = nil
				job.Consumes = []nodeproto.ArtifactInput{{Producer: "producer", Into: "received"}}
				_, err = prepare(t.Context(), nodehost.LayerSpec{Name: "artifacts"}, input, job)
				if !errors.Is(err, backend.failure) || backend.lookup.Key != key || backend.lookup.Authority != input.Storage.Authority {
					t.Fatalf("native artifact consumption bypassed authenticated records: %+v %v", backend.lookup, err)
				}
			} else {
				finish, err := prepare(t.Context(), nodehost.LayerSpec{Name: "artifacts"}, input, job)
				if err != nil || finish == nil {
					t.Fatalf("prepare artifacts: %v", err)
				}
				err = finish(t.Context(), input, &store.NodeResult{Outcome: store.NodeSuccess})
				if !errors.Is(err, backend.failure) || backend.upload.Identity != input.Storage.Authority.Upload.Value || backend.upload.Authority != input.Storage.Authority {
					t.Fatalf("native artifact publication bypassed authenticated bytes: %+v %v", backend.upload, err)
				}
			}
		})
	}
}

func nativeArtifactInput(t testing.TB) (nodehost.NodeInput, *nativeArtifactBackend) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("burst artifact"), 0o640); err != nil {
		t.Fatal(err)
	}
	attempt := runspec.AttemptIdentity{Team: "default", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1}
	optional := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
	upload := blob.UploadIdentity{Kind: blob.UploadJob, Team: "default", RunID: "run", Principal: "account", CapabilityRef: "node", Attempt: optional}
	authority := blob.Authority{CapabilityRef: "node", Principal: "account", Repo: "git:example.test/acme/repo", Attempt: optional, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: upload}}
	accepted := runspec.Spec{Hash: "spec", PlanHash: "plan", Request: runspec.SealedRequest{Request: runspec.Request{Team: "default", Pipeline: "burst", Git: runspec.Git{Repository: runspec.Repository{Key: authority.Repo}}}}}
	raw, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	backend := &nativeArtifactBackend{failure: errors.New("artifact transport boundary")}
	return nodehost.NodeInput{Accepted: nodehost.AcceptedRun{RunID: "run", Spec: raw, SpecHash: "spec", PlanHash: "plan"}, NodeID: "job", Generation: 2, Attempt: 1, Authority: nodehost.ClaimAuthority{Holder: "account", CapabilityPrefix: "node"}, Entry: &source.Entry{Directory: workspace}, Storage: nodehost.StorageBinding{Bytes: backend, Records: backend, Authority: authority}}, backend
}

type nativeArtifactBackend struct {
	blob.Store
	blob.RecordStore
	failure error
	upload  blob.PutRequest
	lookup  blob.RecordLookup
}

func (b *nativeArtifactBackend) Put(_ context.Context, input blob.PutRequest) (blob.Receipt, error) {
	b.upload = input
	return blob.Receipt{}, b.failure
}

func (b *nativeArtifactBackend) LookupRecord(_ context.Context, input blob.RecordLookup) (blob.RecordLookupResult, error) {
	b.lookup = input
	return blob.RecordLookupResult{}, b.failure
}

func TestNativeServicesRefusesCacheWithoutStorage(t *testing.T) {
	cfg, _ := nativeConfigFixture(t)
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
	input.Storage = nodehost.StorageBinding{}
	_, err = services.Config.Executor.PrepareLayer(t.Context(), nodehost.LayerSpec{Name: "caches"}, input, nodeproto.JobSpec{ID: "job", Caches: []nodeproto.CacheSpec{{Name: "modules", Path: "modules"}}})
	if err == nil {
		t.Fatal("artifact wiring silently skipped a declared dependency cache")
	}
}

func FuzzNativeArtifactPublicationKeepsAwardScope(f *testing.F) {
	for field := uint8(0); field < 6; field++ {
		f.Add(field, uint8(1))
	}
	f.Fuzz(func(t *testing.T, field, delta uint8) {
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
		input, backend := nativeArtifactInput(t)
		switch field % 6 {
		case 0:
			input.Accepted.RunID += "-foreign"
		case 1:
			input.NodeID += "-foreign"
		case 2:
			input.Generation += int64(delta) + 1
		case 3:
			input.Attempt += int64(delta) + 1
		default:
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			if field%6 == 4 {
				spec.Request.Request.Pipeline += "-foreign"
			} else {
				spec.Request.Request.Git.Repository.Key += "-foreign"
			}
			input.Accepted.Spec, err = json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
		}
		finish, err := services.Config.Executor.PrepareLayer(t.Context(), nodehost.LayerSpec{Name: "artifacts"}, input, nodeproto.JobSpec{ID: input.NodeID, Artifacts: []string{"result.txt"}})
		if err != nil || finish == nil {
			t.Fatalf("prepare artifacts: %v", err)
		}
		err = finish(t.Context(), input, &store.NodeResult{Outcome: store.NodeSuccess})
		if err == nil || backend.upload.Body != nil {
			t.Fatalf("foreign execution published under another award: %v", err)
		}
	})
}
