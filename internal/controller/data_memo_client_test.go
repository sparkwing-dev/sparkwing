package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestDataMemoClientUsesRealRecordsAndCopy(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.test", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	e, tenant := engineServerFixture(t, deployment.Config{}, func(s *Server) { s.WithDataObjects(objects) })
	award := engineRuntimeClaim(t, e, capability.UnitNode)
	raw := nativeClaimPresentation(t, e, award)
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","memo":{}}`))
	h := e.Server.Handler()
	engineAdmitBearer(t, e.Store, raw)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	authority, err := tenant.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: award.Principal.Capability.Prefix}, capability.DataRead)
	if err != nil {
		t.Fatal(err)
	}
	output := dataResultUpload(t, tenant, objects, authority, authority.Upload.Value, "memo-output", []byte(`{"value":42}`))
	manifestBody, err := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: "result.json", Digest: output.Digest, Size: output.Size, Mode: 0o640}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := dataResultUpload(t, tenant, objects, authority, authority.Upload.Value, "memo-manifest", manifestBody)
	members := []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Digest: output.Digest, Path: "result.json", Size: output.Size, Mode: 0o640}}
	key := blob.RecordKey{Team: "a", Kind: blob.KindMemo, Name: "output", Scope: blob.Scope{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "ci", Job: "job", Key: strings.Repeat("b", 64)}}
	if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Source: authority.Upload.Value, Key: key, Digest: output.Digest, ManifestDigest: runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}, Members: members}); err != nil {
		t.Fatal(err)
	}
	transport := localHostRoundTripper{handler: h}
	_, records, err := client.NewDataClient(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	lookup := blob.MemoLookup{Authority: authority, Key: blob.MemoKey{Repo: key.Scope.Repo, Ref: key.Scope.Ref, Pipeline: "ci", Job: "job", Hash: runspec.Hash(key.Scope.Key)}}
	if hit, err := records.LookupMemo(t.Context(), lookup); err != nil || hit.Found {
		t.Fatalf("unsettled producer reusable: %+v, %v", hit, err)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"success","cause":"report","source_attempt":1,"result":{"outcome":"success"}}`, 200)
	if _, err := records.LookupMemo(t.Context(), lookup); err == nil {
		t.Fatal("completed producer retained live memo authority")
	}
	engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) SELECT team,'memo-reader',2,pipeline,'running',request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,lineage_json,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','memo-reader','job','pending',1,?,?)`, []byte(`{"id":"job","memo":{}}`), []byte(`{}`))
	engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'memo-reader',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE team='a' AND id='run'`)
	engineAcceptedFixturePlan(t, e, "a", "memo-reader")
	reader, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "memo-reader", NodeID: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	raw = string(reader.Bearer)
	engineAdmitBearer(t, e.Store, raw)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/memo-reader/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	authority, err = tenant.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: reader.Award.CapabilityPrefix}, capability.DataRead)
	if err != nil {
		t.Fatal(err)
	}
	lookup.Authority = authority
	for _, operation := range []string{"lookup", "copy", "copy"} {
		var hit blob.MemoHit
		if operation == "lookup" {
			hit, err = records.LookupMemo(t.Context(), lookup)
		} else {
			hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: authority.Attempt.Value})
		}
		if err != nil || !hit.Found || hit.Output.Value != output.Digest || hit.Manifest.Value != manifest.Digest || len(hit.Members) != 1 || hit.Members[0] != members[0] || hit.ReadSet.Reader != authority.Attempt.Value || len(hit.ReadSet.Edges) != 0 {
			t.Fatalf("%s: %+v %v", operation, hit, err)
		}
	}
}
