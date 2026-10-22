package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

func TestNativeHostStorageAndLogsUsePublicRecordReaders(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	engine, tenant := engineServerFixture(t, deployment.Config{}, func(s *Server) { s.WithDataObjects(objects) })
	authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
	raw := nativeClaimPresentation(t, engine, authorized)
	server := engine.Server
	handler := server.HTTP().API
	engineAdmitBearer(t, engine.Store, raw)
	engineClaimHTTP(t, handler, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{handler}}, Bearer: func(context.Context) (string, error) { return raw, nil }}
	host, err := client.NewNodeHostClient(options, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	storage, err := host.BindStorage(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	output := blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: storage.Authority.Upload.Value, Pipeline: run.Request.Request.Pipeline, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}
	record, err := output.Put(t.Context(), []byte(`{"value":"stored"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := output.Read(t.Context(), record.Key)
	if err != nil || string(body) != `{"value":"stored"}` {
		t.Fatalf("canonical output roundtrip: %s %v", body, err)
	}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: principal.Capability.Team, RunID: "run", Attempt: &runspec.AttemptIdentity{Team: principal.Capability.Team, RunID: "run", NodeID: "job", Generation: principal.Capability.Binding.Generation, Attempt: principal.Capability.Binding.Attempt}}
	uploader, err := host.LogUploader(t.Context(), principal, subject)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := logseg.NewSink(t.Context(), uploader, logseg.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append([]byte("public local log\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), "GET", "http://local/api/v1/runs/run/jobs/job/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+raw)
	response, err := options.HTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "public local log") {
		t.Fatalf("public log roundtrip: status=%d %s %v", response.StatusCode, body, err)
	}
}
