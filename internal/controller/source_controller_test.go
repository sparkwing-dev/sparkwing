package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type auditedSourceCipher struct {
	*secrets.Cipher
	store *store.Store
	opens int
	t     *testing.T
}

func (c *auditedSourceCipher) OpenBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, envelope string) (string, error) {
	c.opens++
	var count int
	if err := teststore.SQL(c.t, c.store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='git_credential.released' AND team=?`, team).Scan(&count); err != nil || count != 1 {
		c.t.Fatalf("decrypted before audit commit: count=%d err=%v", count, err)
	}
	return c.Cipher.OpenBound(purpose, team, name, scope, shared, masked, envelope)
}

func TestSourceControllerUsesStoreAuditAndBoundCipher(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "revoked"}[revoked], func(t *testing.T) {
			e, tenant := engineRuntimeStore(t)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Request.Source.Location.RemoteURL = "https://github.com/acme/repo"
			raw, err := json.Marshal(run.Request)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
			a := engineRuntimeClaim(t, e, capability.UnitNode)
			e.claimProfiles = engineProfileFake{}
			engineSQL(t, e.Store, `UPDATE capabilities SET created_by='owner' WHERE prefix='runner'`)
			registerFixtureRunner(t, e.Store, false)
			cipher, err := secrets.NewCipher(bytes.Repeat([]byte{21}, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := cipher.SealBound(secrets.PurposeGitCredential, "a", "git-credential:github.com", "git-credential:host:github.com", false, true, "ephemeral-secret")
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `INSERT INTO git_credentials(host_wide,id,team,host,kind,secret,confirmed_at,created_at,updated_at) VALUES (1,'credential','a','github.com','https',?,1,1,1)`, envelope)
			p := a.Principal.Capability
			issued, err := e.Store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: a.Principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.SourceRead}, Binding: p.Binding, Repositories: p.Repositories, Data: p.Data, ExpiresAt: p.ExpiresAt})
			if err != nil {
				t.Fatal(err)
			}
			if revoked {
				revokeEngineMachine(t, e.Store, "runner")
			}
			audited := &auditedSourceCipher{Cipher: cipher, store: e.Store, t: t}
			h := New(e.Store, nil).WithSecretsCipher(audited).Handler()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run/source-credential", nil)
			request.Header.Set("Authorization", "Bearer "+issued.Raw)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request)
			if revoked {
				if w.Code != http.StatusUnauthorized || audited.opens != 0 {
					t.Fatal("revoked root released a credential")
				}
			} else {
				var got SourceCredentialResponse
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || audited.opens != 1 || got.Secret != "ephemeral-secret" || got.Host != "github.com" {
					t.Fatalf("HTTP %d %s opens=%d", w.Code, w.Body.String(), audited.opens)
				}
			}
		})
	}
}
