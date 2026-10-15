package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sourceTransactions struct {
	authorized       capability.Authorized
	credential       store.GitCredential
	audit            capability.SourceReleaseAudit
	err              error
	releases         int
	consentAuthority capability.AuthorityRef
	consentMachine   string
	consentEnabled   bool
}

func (f *sourceTransactions) ReleaseSourceCredential(_ context.Context, a capability.Authorized) (store.GitCredential, capability.SourceReleaseAudit, error) {
	f.authorized = a
	f.releases++
	return f.credential, f.audit, f.err
}

func (f *sourceTransactions) SetSourceMachineConsent(_ context.Context, a capability.AuthorityRef, machine string, enabled bool) error {
	f.consentAuthority, f.consentMachine, f.consentEnabled = a, machine, enabled
	return f.err
}

type sourceCipher struct {
	opens int
	err   error
}

func (c *sourceCipher) Seal(string) (string, error) { return "", errors.New("unused") }
func (c *sourceCipher) Open(string) (string, error) { return "", errors.New("unbound open") }
func (c *sourceCipher) SealBound(string, string, string, bool, bool, string) (string, error) {
	return "", errors.New("unused")
}
func (c *sourceCipher) OpenBound(team, name, scope string, shared, masked bool, envelope string) (string, error) {
	c.opens++
	if team != "team" || name != "git-credential:git.example" || scope != "git-credential" || shared || !masked || envelope != "private-envelope" {
		return "", errors.New("private-cipher-binding")
	}
	return "private-source-secret", c.err
}

func sourceHTTPFixture(t *testing.T, b *credentialBoundary) (*sourceTransactions, *sourceCipher, http.Handler) {
	t.Helper()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Grants = append(p.Capability.Grants, capability.SourceRead)
	p.Capability.Repositories = []string{"host:git.example/org/repo"}
	p.Capability.Data = &capability.DataScope{RepositoryKey: p.Capability.Repositories[0]}
	tx := &sourceTransactions{
		credential: store.GitCredential{ID: "credential", Team: "team", Host: "git.example", Kind: store.GitCredentialHTTPS, Username: "git", Secret: "private-envelope", ConfirmedAt: &b.snapshot.Now},
		audit:      capability.SourceReleaseAudit{ID: "committed-audit", Team: "team", CredentialID: "credential", Host: "git.example", RepositoryKey: p.Capability.Repositories[0], RunID: p.Capability.Binding.RunID, NodeID: p.Capability.Binding.NodeID, Generation: p.Capability.Binding.Generation, CapabilityPrefix: p.Capability.Prefix, MachineID: "machine", ReleasedAt: b.snapshot.Now},
	}
	cipher := &sourceCipher{}
	e := fakeEngine(t, &engineTransactions{})
	source := &controller.SourceController{Cipher: cipher, ResolveTenant: func(_ context.Context, team store.Team) (controller.SourceTransactions, error) {
		if team != "team" {
			t.Fatalf("source tenant = %q", team)
		}
		return tx, nil
	}}
	h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithEngineController(e).WithSourceController(source).Handler()
	return tx, cipher, h
}

func TestSourceReleaseHTTPUsesCommittedAuditBeforeOpening(t *testing.T) {
	b := newCredentialBoundary()
	tx, cipher, h := sourceHTTPFixture(t, b)
	w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/source-credential?host=foreign&team=foreign", "")
	var got controller.SourceCredentialResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Secret != "private-source-secret" || got.Host != "git.example" || got.Kind != store.GitCredentialHTTPS || got.Username != "git" || cipher.opens != 1 || tx.releases != 1 {
		t.Fatalf("HTTP %d %s opens=%d releases=%d", w.Code, w.Body.String(), cipher.opens, tx.releases)
	}
	if tx.authorized.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || tx.authorized.Target.Team != "team" || tx.authorized.Target.RunID != "run" || tx.authorized.Access.Binding != capability.BindSource || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authorization=%+v headers=%v", tx.authorized, w.Header())
	}
	for _, private := range []string{"private-envelope", "committed-audit", "credential_id", "capability_prefix"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private release metadata reached response")
		}
	}
}

func TestSourceReleaseHTTPRefusalsNeverOpenCredential(t *testing.T) {
	for _, state := range []string{"missing", "revoked", "parent revoked", "finished", "stale generation", "cancelled", "missing grant", "binding", "transaction", "audit", "cipher", "body"} {
		t.Run(state, func(t *testing.T) {
			b := newCredentialBoundary()
			tx, cipher, h := sourceHTTPFixture(t, b)
			status, code, body := 401, apierr.Unauthenticated, ""
			switch state {
			case "missing":
				b.raw = ""
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "parent revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "finished":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "stale generation":
				b.snapshot.Binding.Current.Generation++
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
			case "missing grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.NodeReport}
				status, code = 403, apierr.Forbidden
			case "binding":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "binding refused"}
				status, code = 403, apierr.Forbidden
			case "transaction":
				tx.err = &apierr.Error{Code: apierr.Forbidden, Message: "source release refused"}
				status, code = 403, apierr.Forbidden
			case "audit":
				tx.audit.ID = ""
				status, code = 500, apierr.Internal
			case "cipher":
				cipher.err = errors.New("private-decryption-error")
				status, code = 500, apierr.Internal
			case "body":
				body = `{"host":"foreign"}`
				status, code = 400, apierr.BadRequest
			}
			w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/source-credential", body)
			assertEngineError(t, w, status, code)
			if state != "cipher" && cipher.opens != 0 || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("opens=%d body=%s", cipher.opens, w.Body.String())
			}
		})
	}
}

func FuzzSourceReleaseHTTPAuditCannotChangeCredentialScope(f *testing.F) {
	f.Add("foreign", uint8(0))
	f.Add("", uint8(7))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		b := newCredentialBoundary()
		tx, cipher, h := sourceHTTPFixture(t, b)
		foreign := "foreign-" + value
		switch field % 8 {
		case 0:
			tx.audit.Team = foreign
		case 1:
			tx.audit.CredentialID = foreign
		case 2:
			tx.audit.Host = foreign
		case 3:
			tx.audit.RepositoryKey = foreign
		case 4:
			tx.audit.RunID = foreign
		case 5:
			tx.audit.NodeID = foreign
		case 6:
			tx.audit.CapabilityPrefix = foreign
		case 7:
			tx.audit.Generation++
		}
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/source-credential", "")
		assertEngineError(t, w, 500, apierr.Internal)
		if cipher.opens != 0 || strings.Contains(w.Body.String(), "private-") {
			t.Fatal("unbound audit opened or leaked a credential")
		}
	})
}

func TestSourceMachineConsentHTTPUsesAccountAuthority(t *testing.T) {
	b := authorityMember()
	p := &b.snapshot.Authorities[0].Principal
	p.Class, p.Authority = capability.CredentialSession, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "account-session"}
	tx, _, h := sourceHTTPFixture(t, b)
	w := authorityHTTP(h, b, "PUT", "/api/v1/tokens/machine/git-credentials", `{"enabled":true}`)
	if w.Code != 200 || tx.consentAuthority != b.snapshot.Authorities[0].Principal.Authority || tx.consentMachine != "machine" || !tx.consentEnabled || tx.releases != 0 {
		t.Fatalf("HTTP %d %s tx=%+v", w.Code, w.Body.String(), tx)
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":true,"account":"other"}`} {
		w = authorityHTTP(h, b, "PUT", "/api/v1/tokens/machine/git-credentials", body)
		assertEngineError(t, w, 400, apierr.BadRequest)
	}
}
