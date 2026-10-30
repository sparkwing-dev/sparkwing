package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
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
	if testing.Short() {
		t.Skip("slow: 1.2s measured in the short class; the full suite runs it")
	}
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "revoked"}[revoked], func(t *testing.T) {
			e, tenant := engineRuntimeStore(t)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Request.Source.Location.RemoteURL = "https://github.com/acme/repo"
			run.Request.Provenance, err = run.Request.Provenance.FetchedFrom(1, "acme/repo", "", "")
			if err != nil {
				t.Fatal(err)
			}
			run.Request.Provenance, err = run.Request.Provenance.WithSourceCredentialVerified()
			if err != nil {
				t.Fatal(err)
			}
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

type sourceMintTransactions struct {
	phase  int
	refuse bool
}

func (*sourceMintTransactions) Team() store.Team { return "a" }

func (f *sourceMintTransactions) PrepareGitHubSourceRelease(context.Context, capability.Authorized) (store.GitHubSourceGrant, error) {
	f.phase = 1
	return store.GitHubSourceGrant{Installation: 7, Repositories: []int64{1, 2}, Names: []string{"acme/repo", "acme/extra"}}, nil
}

func (f *sourceMintTransactions) CommitGitHubSourceRelease(_ context.Context, _ capability.Authorized, grant store.GitHubSourceGrant, expires time.Time) error {
	if f.phase != 2 || grant.Installation != 7 || !slices.Equal(grant.Repositories, []int64{1, 2}) || expires.IsZero() {
		return errors.New("invalid release ordering or grant")
	}
	f.phase = 3
	if f.refuse {
		return errors.New("consent revoked during mint")
	}
	return nil
}

type sourceMintProvider struct{ transactions *sourceMintTransactions }

func (f sourceMintProvider) InstallationTokenByIDs(_ context.Context, installation int64, repositories []int64, permissions map[string]string) (githubapp.Token, error) {
	if f.transactions.phase != 1 || installation != 7 || !slices.Equal(repositories, []int64{1, 2}) || len(permissions) != 1 || permissions["contents"] != "read" {
		return githubapp.Token{}, errors.New("token mint preceded authorization or widened its grant")
	}
	f.transactions.phase = 2
	return githubapp.Token{Token: "ephemeral-token", ExpiresAt: time.Unix(2000000000, 0)}, nil
}

func TestSourceControllerReturnsMintedTokenOnlyAfterReleaseCommit(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			transactions := &sourceMintTransactions{refuse: refuse}
			controller := SourceController{GitHub: sourceMintProvider{transactions}, ResolveTenant: func(context.Context, store.Team) (TenantHandle, error) { return transactions, nil }}
			got, err := controller.release(t.Context(), capability.Authorized{Target: capability.Target{Team: "a"}})
			if transactions.phase != 3 {
				t.Fatalf("release stopped at phase %d: %v", transactions.phase, err)
			}
			if refuse {
				if err == nil || got.Secret != "" {
					t.Fatal("uncommitted token escaped")
				}
			} else if err != nil || got.Secret != "ephemeral-token" || !slices.Equal(got.ExtraRepositories, []string{"acme/extra"}) {
				t.Fatalf("release: %+v %v", got, err)
			}
		})
	}
}
