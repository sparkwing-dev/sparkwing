package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCloudLogsSealFiftyMillionPayloadBytes(t *testing.T) {
	e, _, _ := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	h := e.Server.WithDataObjects(objects).Handler()
	tenant := teststore.ForTeam(t, e.Store, "a")
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "large-logs", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	response = engineClaimHTTP(t, h, award.Claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, http.StatusOK)
	var exchange controllerwire.ExchangeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &exchange); err != nil {
		t.Fatal(err)
	}
	claim := exchange.Claim
	sync := func() controllerwire.LauncherSyncResponse {
		t.Helper()
		fence := capability.LeaseRef{Kind: capability.UnitNode, ID: claim.UnitID, Holder: claim.Binding.Holder, Generation: claim.Binding.Generation}
		body, err := json.Marshal(controllerwire.LauncherSyncRequest{Observed: []capability.LauncherObservation{{Award: fence}}})
		if err != nil {
			t.Fatal(err)
		}
		response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", string(body), http.StatusOK)
		var result controllerwire.LauncherSyncResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{h}}, Bearer: func(context.Context) (string, error) { return claim.Capability, nil }}
	host, err := client.NewNodeHostClient(options, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	engineAdmitBearer(t, e.Store, claim.Capability)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
	uploader, err := host.LogUploader(t.Context(), principal, subject)
	if err != nil {
		t.Fatal(err)
	}
	sealObserved := false
	uploader = observedCloudLogSeal{Uploader: uploader, before: func() {
		sealObserved = true
		var persisted bytes.Buffer
		read, err := (logseg.CommittedReader{Records: tenant, Objects: objects}).Read(t.Context(), engineActor(capability.Authorized{Principal: principal, Binding: claim.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}), blob.LogReadRequest{Subject: subject}, &persisted)
		if err != nil || read.Complete || bytes.Count(persisted.Bytes(), []byte("x")) != 50_000_000 || !bytes.Contains(persisted.Bytes(), []byte("EMIT_DONE")) {
			t.Fatalf("payload before final seal: complete=%v bytes=%d err=%v", read.Complete, persisted.Len(), err)
		}
		if result := sync(); len(result.Retired) != 0 || len(result.Deferred) != 1 {
			t.Errorf("launcher would kill the successful Cloud job before its final log seal: %+v", result)
		}
	}}
	sink, err := logseg.NewSink(t.Context(), uploader, logseg.Options{Subject: subject, Retries: 1})
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 50_000) + "\n")
	for range 1000 {
		if err := sink.Append(line, "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Append([]byte("EMIT_DONE\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"success","cause":"report","source_attempt":1,"result":{"outcome":"success"}}`, http.StatusOK)
	manifest, err := sink.Close(t.Context(), logseg.End{})
	if err != nil || manifest.Status != logseg.StatusComplete || !sealObserved {
		t.Fatalf("seal: %s %v", manifest.Status, err)
	}
	if result := sync(); len(result.Retired) != 1 {
		t.Fatalf("sealed Cloud job not retired: %+v", result)
	}
	var output bytes.Buffer
	read, err := (logseg.CommittedReader{Records: tenant, Objects: objects}).Read(t.Context(), engineActor(capability.Authorized{Principal: principal, Binding: claim.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}), blob.LogReadRequest{Subject: subject}, &output)
	if err != nil || !read.Complete || bytes.Count(output.Bytes(), []byte("x")) != 50_000_000 || !bytes.Contains(output.Bytes(), []byte("EMIT_DONE")) {
		t.Fatalf("large Cloud log: complete=%v bytes=%d err=%v", read.Complete, output.Len(), err)
	}
}

type observedCloudLogSeal struct {
	logseg.Uploader
	before func()
}

func (u observedCloudLogSeal) PutManifest(ctx context.Context, manifest logseg.Manifest) error {
	u.before()
	return u.Uploader.PutManifest(ctx, manifest)
}
