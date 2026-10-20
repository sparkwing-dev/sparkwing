package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestEveryDataEndpointDecodesBinding(t *testing.T) {
	s := New(nil, nil)
	digest := strings.Repeat("a", 64)
	bodies := map[string]string{
		"POST /api/v1/data/upload":          `{"digest":"` + digest + `","size":0}`,
		"POST /api/v1/data/commit":          `{"upload_id":"ticket"}`,
		"POST /api/v1/data/records":         `{"kind":"memo","name":"hash","digest":"` + digest + `"}`,
		"GET /api/v1/data/records":          ``,
		"POST /api/v1/data/download":        `{"digest":"` + digest + `"}`,
		"POST /api/v1/data/has-many":        `{"digests":["` + digest + `"]}`,
		"POST /api/v1/data/memo/lookup":     `{"pipeline":"ci","job":"build","hash":"hash"}`,
		"POST /api/v1/data/depcache/lookup": `{"name":"cache","key":"key"}`,
		"POST /api/v1/data/memo/copy":       `{"pipeline":"ci","job":"build","hash":"hash"}`,
	}
	for _, endpoint := range s.dataEndpoints() {
		t.Run(endpoint.Method+" "+endpoint.Pattern, func(t *testing.T) {
			path := endpoint.Pattern
			if endpoint.Method == "GET" {
				path += "?kind=memo&name=hash"
			}
			r := httptest.NewRequest(endpoint.Method, path, strings.NewReader(bodies[endpoint.Method+" "+endpoint.Pattern]))
			r.Header.Set("Content-Type", "application/json")
			evidence, err := endpoint.Decode(r)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Target.Data == nil {
				t.Fatal("missing data authorization selector")
			}
			if endpoint.ResolveTarget == nil {
				t.Fatal("missing credential scope resolution")
			}
			p := capability.Principal{Capability: capability.Capability{Data: &capability.DataScope{RunID: "run", NodeID: "job", Attempt: 2, Generation: 3}}}
			target, err := endpoint.ResolveTarget(t.Context(), p, evidence.Target)
			if err != nil || target.Data.RunID != "run" || target.Data.NodeID != "job" || target.Data.Attempt != 2 || target.Data.Generation != 3 {
				t.Fatalf("scope=%+v error=%v", target.Data, err)
			}
		})
	}
}

func TestNativeDataReaderRoutesAndSelectorIsolation(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	st := e.Store
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('data-reader','data@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','data-reader','reader',1)`)
	raw := engineHTTPRoot(t, e, "a", "data-reader", []capability.Grant{capability.DataRead})
	p, err := st.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	c := p.Capability
	c.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "data-reader"}
	c.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, `UPDATE capabilities SET subject_kind='account',subject_id='data-reader',repo_key='gh:1',authority_json=? WHERE prefix='data-reader'`, envelope)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.test", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	h := New(st, nil).WithDataObjects(objects).Handler()
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/api/v1/data/has-many", `{"digests":[]}`},
		{"GET", "/api/v1/data/records?kind=memo&name=output&key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&ref=main&pipeline=ci&job=build", ""},
		{"POST", "/api/v1/data/memo/lookup", `{"pipeline":"ci","job":"build","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{"POST", "/api/v1/data/depcache/lookup", `{"name":"absent","key":"none"}`},
	} {
		engineClaimHTTP(t, h, raw, request.method, request.path, request.body, 200)
	}
	property := func(input []byte) bool {
		body, _ := json.Marshal(map[string]any{"pipeline": "ci", "job": "build", "hash": "absent", "repo": base64.RawURLEncoding.EncodeToString(input)})
		r := httptest.NewRequest("POST", "/api/v1/data/memo/lookup", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 400
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, `DELETE FROM memberships WHERE team='a' AND account_id='data-reader'`)
	if err := quick.Check(func(input []byte) bool {
		body := fmt.Sprintf(`{"pipeline":"ci","job":"build","hash":"%x"}`, sha256.Sum256(input))
		r := httptest.NewRequest("POST", "/api/v1/data/memo/lookup", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 401
	}, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDataWritesBindTicketsAndRecords(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git.Ref = "main"
	sealed := fixtureReseal(t, "ci", run.Request)
	engineSQL(t, e.Store, `UPDATE runs SET git_branch='main',request_json=? WHERE team='a' AND id='run'`, sealed)
	a := engineRuntimeClaim(t, e, capability.UnitNode)
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","memo":{}}`))
	raw := nativeClaimPresentation(t, e, a)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.test", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	h := New(e.Store, nil).WithDataObjects(objects).Handler()
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	digest := fmt.Sprintf("%x", sha256.Sum256(nil))
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/data/upload", `{"digest":"`+digest+`","size":0}`, 200)
	var ticket dataUploadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest("PUT", ticket.URL, strings.NewReader(""))
	for key, value := range ticket.Headers {
		put.Header.Set(key, value)
	}
	putResult := httptest.NewRecorder()
	h.ServeHTTP(putResult, put)
	if putResult.Code != 201 {
		t.Fatalf("put=%d %s", putResult.Code, putResult.Body.String())
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/data/commit", `{"upload_id":"`+ticket.UploadID+`"}`, 200)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/data/records", `{"kind":"output","pipeline":"ci","job":"job","name":"result","key":"run/1/1","digest":"`+digest+`"}`, 200)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/data/download", `{"digest":"`+digest+`"}`, 200)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/data/memo/copy", `{"pipeline":"ci","job":"job","hash":"`+digest+`"}`, 200)
}
