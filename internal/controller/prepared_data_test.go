package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPreparedDataHTTPPublishAndUnstartedAwardRead(t *testing.T) {
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
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git.Ref = "main"
	sealed := sealWith(t, run.Request, assertedProvenance(t, "ci", "main", run.Request.Request.Git.SHA))
	engineSQL(t, e.Store, "UPDATE runs SET git_branch='main',request_json=? WHERE team='a' AND id='run'", sealed)
	owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
	ownerRaw := nativeClaimPresentation(t, e, owner)
	h := e.Server.Handler()
	body := "prepared executable and source"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	w := engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/upload", fmt.Sprintf(`{"digest":%q,"size":%d}`, digest, len(body)), 200)
	var upload dataUploadResponse
	if err = json.Unmarshal(w.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, upload.URL, strings.NewReader(body))
	for k, v := range upload.Headers {
		put.Header.Set(k, v)
	}
	putResult := httptest.NewRecorder()
	h.ServeHTTP(putResult, put)
	if putResult.Code != 201 {
		t.Fatalf("PUT: %d %s", putResult.Code, putResult.Body.String())
	}
	engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/commit", fmt.Sprintf(`{"upload_id":%q}`, upload.UploadID), 200)
	engineClaimHTTP(t, h, ownerRaw, "POST", "/api/v1/data/records", fmt.Sprintf(`{"kind":"prepared","pipeline":"ci","name":"pipeline","key":"run","digest":%q}`, digest), 200)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	node := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	nodeRaw := nativeClaimPresentation(t, e, node)
	if !strings.Contains(node.Principal.Capability.Data.WriteRef, "host-") {
		t.Fatalf("fixture has no asserted host partition: %s", node.Principal.Capability.Data.WriteRef)
	}
	engineClaimHTTP(t, h, nodeRaw, "GET", "/api/v1/data/records?kind=prepared&pipeline=ci&name=pipeline&key=other-run", "", 403)
	w = engineClaimHTTP(t, h, nodeRaw, "GET", "/api/v1/data/records?kind=prepared&pipeline=ci&name=pipeline&key=run", "", 200)
	var hit controllerwire.DataRecordLookupResult
	if err = json.Unmarshal(w.Body.Bytes(), &hit); err != nil || !hit.Found || hit.Record == nil || string(hit.Record.Digest) != digest {
		t.Fatalf("lookup=%s: %v", w.Body.String(), err)
	}
	w = engineClaimHTTP(t, h, nodeRaw, "POST", "/api/v1/data/download", fmt.Sprintf(`{"digest":%q}`, digest), 200)
	var download DataDownloadResponse
	if err = json.Unmarshal(w.Body.Bytes(), &download); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, download.URL, nil)
	for k, v := range download.Headers {
		get.Header.Set(k, v)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	if got.Code != 200 || got.Body.String() != body {
		t.Fatalf("GET=%d %q", got.Code, got.Body.String())
	}
}
