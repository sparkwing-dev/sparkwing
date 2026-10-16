package acceptance_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestEmbeddedOwnerSealsLogsDuringCancelledHostCleanup(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitTrigger)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	handler := controller.New(s, nil).WithDataObjects(objects).HTTP().API
	transport := &http.Client{Transport: surfaceTransport(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := errors.New("host cancelled after log append")
	var principal capability.Principal
	var subject blob.LogSubject
	var retired func(context.Context) (string, error)
	var cleanupErr error
	host := runners.StoreHosting{Store: s, Bind: func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
		retired, principal = binding.Bearer, binding.Principal
		private, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://local", HTTP: transport, Bearer: binding.Bearer}, unit.RunID)
		if err != nil {
			return runners.NativeServices{}, err
		}
		t.Cleanup(func() { _ = private.Close() })
		subject = blob.LogSubject{Kind: blob.LogOwnerUnit, Team: principal.Capability.Team, RunID: unit.RunID, Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: principal.Capability.Binding.Generation}}
		uploader, err := private.LogUploader(ctx, principal, subject)
		if err != nil {
			return runners.NativeServices{}, err
		}
		sink, err := logseg.NewSink(ctx, uploader, logseg.Options{Subject: subject})
		if err != nil {
			return runners.NativeServices{}, err
		}
		t.Cleanup(func() { _, _ = sink.Close(context.WithoutCancel(ctx), logseg.End{}) })
		if err := sink.Append([]byte("last owner line\n"), "", 0, 0); err != nil {
			return runners.NativeServices{}, err
		}
		cancel()
		return runners.NativeServices{Close: func() error {
			_, err := sink.Close(context.WithoutCancel(ctx), logseg.End{})
			cleanupErr = errors.Join(err, private.Close())
			return cleanupErr
		}}, finished
	}}
	if err := host.Host(ctx, unit); !errors.Is(err, finished) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if cleanupErr != nil {
		t.Fatalf("cancelled owner could not seal final logs: %v", cleanupErr)
	}
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	actor := store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Holder: principal.Capability.Binding.Holder, Generation: principal.Capability.Binding.Generation}
	result, err := (logseg.CommittedReader{Records: tenant, Objects: objects}).Read(t.Context(), actor, blob.LogReadRequest{Subject: subject}, &output)
	if err != nil || !result.Complete || output.String() != "last owner line\n" {
		t.Fatalf("cancelled owner lost persisted final logs: complete=%t output=%q: %v", result.Complete, output.String(), err)
	}
	if raw, err := retired(t.Context()); raw != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("closed log services retained their bearer")
	}
}
