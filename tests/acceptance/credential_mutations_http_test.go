package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func credentialMutationStore(t *testing.T) (*store.Store, *secrets.Cipher) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "credentials.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, team := range []store.Team{"team", "foreign"} {
		if err := s.AsOperator().CreateTeam(t.Context(), team); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{34}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return s, cipher
}

type credentialMutationFake struct {
	*store.Tenant
	err   error
	calls int
	actor capability.Authorized
}

func (f *credentialMutationFake) record(a capability.Authorized) error {
	f.calls++
	f.actor = a
	return f.err
}

func (f *credentialMutationFake) WriteSecret(_ context.Context, a capability.Authorized, row store.Secret) error {
	if err := f.record(a); err != nil {
		return err
	}
	return f.Tenant.CreateOrReplaceSecret(row, time.Now())
}

func (f *credentialMutationFake) RemoveSecret(_ context.Context, a capability.Authorized, name, pipeline string) error {
	if err := f.record(a); err != nil {
		return err
	}
	return f.Tenant.DeleteSecret(name, pipeline)
}

func (f *credentialMutationFake) WriteGitCredential(ctx context.Context, a capability.Authorized, row store.GitCredential) (store.GitCredential, error) {
	if err := f.record(a); err != nil {
		return store.GitCredential{}, err
	}
	return f.Tenant.PutGitCredential(ctx, row, time.Now())
}

func (f *credentialMutationFake) ConfirmGitCredentialFor(ctx context.Context, a capability.Authorized, host, fingerprint string) (store.GitCredential, error) {
	if err := f.record(a); err != nil {
		return store.GitCredential{}, err
	}
	return f.Tenant.ConfirmGitCredential(ctx, host, fingerprint, time.Now())
}

func (f *credentialMutationFake) RemoveGitCredential(ctx context.Context, a capability.Authorized, host string) error {
	if err := f.record(a); err != nil {
		return err
	}
	return f.Tenant.DeleteGitCredential(ctx, host)
}

func mutationServerConfig(s *store.Store, cipher controller.Cipher, b *credentialBoundary) *controller.Server {
	server := controller.New(s, slog.New(slog.NewTextHandler(io.Discard, nil))).WithSecretsCipher(cipher).WithAuthorization(consoleAuthorization(b))
	if s != nil {
		server.WithCredentialWriter(func(ctx context.Context, team store.Team) (controller.CredentialWriter, error) {
			tenant, err := s.ForTeam(ctx, team)
			return &credentialMutationFake{Tenant: tenant}, err
		})
	}
	return server
}

func mutationServer(s *store.Store, cipher controller.Cipher, b *credentialBoundary) http.Handler {
	return mutationServerConfig(s, cipher, b).Handler()
}

func TestCredentialMutationsHTTPSelectAuthenticatedTenant(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	b := authorityMember()
	h := mutationServer(s, cipher, b)
	property := func(value, selector string, masked, shared bool) bool {
		pipeline := "pipeline-" + selector
		query := url.Values{"team": {selector}, "pipeline": {pipeline}, "run": {selector}}
		target, err := s.ForTeam(t.Context(), "team")
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := s.ForTeam(t.Context(), "foreign")
		if err != nil {
			t.Fatal(err)
		}
		if err := foreign.CreateOrReplaceSecret(store.Secret{Name: "KEY", Pipeline: pipeline, Value: "foreign-value"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{"name": "KEY", "value": value, "pipeline": pipeline, "masked": masked, "shared": shared})
		if err != nil {
			t.Fatal(err)
		}
		w := authorityHTTP(h, b, "POST", "/api/v1/secrets?"+query.Encode(), string(body))
		row, err := target.GetSecretRow("KEY", pipeline)
		if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" || err != nil || row.Principal != "account" || row.Masked != masked || row.Shared != shared {
			return false
		}
		plain, err := cipher.OpenBound("team", "KEY", pipeline, shared, masked, row.Value)
		if err != nil || plain != value || row.Value == value {
			return false
		}
		if _, err := cipher.OpenBound("foreign", "KEY", pipeline, shared, masked, row.Value); err == nil {
			return false
		}
		w = authorityHTTP(h, b, "DELETE", "/api/v1/secrets/KEY?"+query.Encode(), "")
		if _, err := target.GetSecretRow("KEY", pipeline); w.Code != 204 || !errors.Is(err, store.ErrNotFound) {
			return false
		}
		other, err := foreign.GetSecretRow("KEY", pipeline)
		if err != nil || other.Value != "foreign-value" {
			return false
		}
		return foreign.DeleteSecret("KEY", pipeline) == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMutationsHTTPGitValuesStayPrivateAndTenantBound(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	b := authorityMember()
	h := mutationServer(s, cipher, b)
	target, err := s.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.ForTeam(t.Context(), "foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.PutGitCredential(t.Context(), store.GitCredential{Host: "git.invalid", Kind: store.GitCredentialHTTPS, Secret: "foreign-value"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	property := func(value, selector string) bool {
		digest := sha256.Sum256([]byte(value))
		token := "private-" + hex.EncodeToString(digest[:])
		body, err := json.Marshal(map[string]string{"host": "Git.Invalid", "kind": "https", "token": token})
		if err != nil {
			t.Fatal(err)
		}
		query := url.Values{"team": {selector}, "host": {selector}, "run": {selector}}
		w := authorityHTTP(h, b, "POST", "/api/v1/team/git-credentials?"+query.Encode(), string(body))
		row, err := target.GitCredentialForHost(t.Context(), "git.invalid")
		var public controller.GitCredentialJSON
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &public) != nil || !public.Confirmed || public.Host != "git.invalid" || public.CreatedBy != "account" || err != nil || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), row.Secret) {
			return false
		}
		plain, err := cipher.OpenBound("team", "git-credential:git.invalid", "git-credential", false, true, row.Secret)
		if err != nil || plain != token || row.Username != "x-access-token" {
			return false
		}
		if _, err := cipher.OpenBound("foreign", "git-credential:git.invalid", "git-credential", false, true, row.Secret); err == nil {
			return false
		}
		w = authorityHTTP(h, b, "DELETE", "/api/v1/team/git-credentials/git.invalid?"+query.Encode(), "")
		if _, err := target.GitCredentialForHost(t.Context(), "git.invalid"); w.Code != 204 || !errors.Is(err, store.ErrNotFound) {
			return false
		}
		other, err := foreign.GitCredentialForHost(t.Context(), "git.invalid")
		return err == nil && other.Secret == "foreign-value"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

var credentialMutationRequests = []struct{ method, path, body string }{
	{"POST", "/api/v1/secrets", `{"name":"KEY","value":"private credential"}`},
	{"DELETE", "/api/v1/secrets/KEY", ""},
	{"POST", "/api/v1/team/git-credentials", `{"host":"git.invalid","kind":"https","token":"private-credential"}`},
	{"POST", "/api/v1/team/git-credentials/git.invalid/confirm", `{"fingerprint":"SHA256:pin"}`},
	{"DELETE", "/api/v1/team/git-credentials/git.invalid", ""},
}

func TestCredentialMutationsHTTPRefuseUnrelatedAuthorityBeforeStorage(t *testing.T) {
	for _, route := range credentialMutationRequests {
		for _, identity := range []string{"anonymous", "revoked", "expired", "membership", "read only", "machine", "work", "trigger", "bootstrap", "binding"} {
			t.Run(route.method+route.path+"/"+identity, func(t *testing.T) {
				b := authorityMember()
				p := &b.snapshot.Authorities[0].Principal
				code := apierr.Forbidden
				switch identity {
				case "anonymous":
					b.raw, code = "", apierr.Unauthenticated
				case "revoked":
					p.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "expired":
					p.Capability.ExpiresAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "membership":
					b.snapshot.Authorities[0].MembershipValid, code = false, apierr.Unauthenticated
				case "read only":
					p.Capability.Grants = []capability.Grant{capability.RunsRead}
				case "machine":
					p.Capability.Subject.Kind = capability.SubjectMachine
				case "work", "trigger", "bootstrap":
					b = newCredentialBoundary()
					if identity == "trigger" {
						b = ownerBoundary()
					}
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TeamAdmin}
					if identity == "bootstrap" {
						b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
					}
				case "binding":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "outside team"}
				}
				w := authorityHTTP(mutationServer(nil, nil, b), b, route.method, route.path, route.body)
				assertEngineError(t, w, apierr.Status(code), code)
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("credential refusal is cacheable")
				}
			})
		}
	}
}

func TestCredentialMutationsHTTPSessionsRequireCSRFBeforeStorage(t *testing.T) {
	for _, route := range credentialMutationRequests {
		b := authorityMember()
		p := &b.snapshot.Authorities[0].Principal
		p.Class, p.Authority.Kind = capability.CredentialSession, capability.AuthoritySession
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mutationServer(nil, nil, b).ServeHTTP(w, r)
		assertEngineError(t, w, 403, apierr.Forbidden)
	}
}

func TestCredentialMutationsHTTPTransactionBoundary(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	tenant, err := s.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range credentialMutationRequests {
		for _, failure := range []string{"revoked", "missing grant", "missing", "quota", "private", "resolver", "foreign", "nil", "default"} {
			t.Run(route.method+route.path+"/"+failure, func(t *testing.T) {
				b := authorityMember()
				f := &credentialMutationFake{Tenant: tenant}
				code, writes := apierr.Internal, 1
				var resolveErr error
				var writer controller.CredentialWriter = f
				switch failure {
				case "revoked":
					code = apierr.Unauthenticated
					f.err = &apierr.Error{Code: code, Message: "authority revoked before commit"}
				case "missing grant":
					code = apierr.Forbidden
					f.err = &apierr.Error{Code: code, Message: "membership changed before commit", Details: map[string]any{"reason": apierr.ReasonMissingGrant}}
				case "missing":
					code, f.err = apierr.NotFound, store.ErrNotFound
				case "quota":
					if strings.Contains(route.path, "/git-credentials") {
						code, f.err = apierr.Conflict, store.ErrGitCredentialLimit
					} else {
						code, f.err = apierr.PayloadTooLarge, store.ErrSecretLimit
					}
				case "private":
					f.err = errors.New("private credential diagnostic")
				case "resolver":
					resolveErr, writes = errors.New("private credential resolver"), 0
				case "foreign":
					f.Tenant, err = s.ForTeam(t.Context(), "foreign")
					if err != nil {
						t.Fatal(err)
					}
					code, writes = apierr.PolicyMissing, 0
				case "nil", "default":
					writer, code, writes = nil, apierr.PolicyMissing, 0
				}
				server := mutationServerConfig(s, cipher, b).WithCredentialWriter(func(_ context.Context, team store.Team) (controller.CredentialWriter, error) {
					if team != "team" {
						t.Fatalf("resolved tenant %q", team)
					}
					return writer, resolveErr
				})
				if failure == "default" {
					server.WithCredentialWriter(nil)
				}
				w := authorityHTTP(server.Handler(), b, route.method, route.path, route.body)
				assertEngineError(t, w, apierr.Status(code), code)
				if f.calls != writes {
					t.Fatalf("transaction calls=%d want %d", f.calls, writes)
				}
				if writes != 0 && (f.actor.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || f.actor.Target.Team != "team" || f.actor.Principal.Capability.Subject.ID != "account") {
					t.Fatalf("transaction lost authority: %+v", f.actor)
				}
			})
		}
	}
}

func TestCredentialMutationsHTTPConfirmationUsesTenantAndPin(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	b := authorityMember()
	for _, team := range []store.Team{"team", "foreign"} {
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.PutGitCredential(t.Context(), store.GitCredential{Host: "git.invalid", Kind: store.GitCredentialSSH, Secret: "private-ssh-key", Fingerprint: "SHA256:pin"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h := mutationServer(s, cipher, b)
	path := "/api/v1/team/git-credentials/git.invalid/confirm?team=foreign"
	w := authorityHTTP(h, b, "POST", path, `{"fingerprint":"SHA256:wrong"}`)
	assertEngineError(t, w, 409, apierr.Conflict)
	for range 2 {
		w = authorityHTTP(h, b, "POST", path, `{"fingerprint":" SHA256:pin "}`)
		var public controller.GitCredentialJSON
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &public) != nil || !public.Confirmed || public.Fingerprint != "SHA256:pin" || strings.Contains(w.Body.String(), "private-ssh-key") {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	foreign, err := s.ForTeam(t.Context(), "foreign")
	if err != nil {
		t.Fatal(err)
	}
	row, err := foreign.GitCredentialForHost(t.Context(), "git.invalid")
	if err != nil || row.ConfirmedAt != nil {
		t.Fatalf("foreign row changed: %+v %v", row, err)
	}
}

func TestCredentialMutationsHTTPValidatesBodiesBeforeTransactions(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	tenant, err := s.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range credentialMutationRequests {
		if route.method != "POST" {
			continue
		}
		for _, body := range []string{"", "null", "[]", "true", `{"team":"foreign"}`, route.body + `{}`, strings.TrimSuffix(route.body, "}") + `,"actor":"forged"}`} {
			b := authorityMember()
			f := &credentialMutationFake{Tenant: tenant}
			server := mutationServerConfig(s, cipher, b).WithCredentialWriter(func(context.Context, store.Team) (controller.CredentialWriter, error) { return f, nil })
			w := authorityHTTP(server.Handler(), b, route.method, route.path, body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if f.calls != 0 {
				t.Fatal("invalid body reached transaction")
			}
		}
	}
	for _, name := range []string{"", "new..name", "KEY' OR 1=1 --"} {
		b := authorityMember()
		if name != "" {
			if err := tenant.CreateOrReplaceSecret(store.Secret{Name: name, Value: "unchanged"}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		body, err := json.Marshal(map[string]string{"name": name, "value": "private credential"})
		if err != nil {
			t.Fatal(err)
		}
		w := authorityHTTP(mutationServer(s, cipher, b), b, "POST", "/api/v1/secrets", string(body))
		assertEngineError(t, w, 400, apierr.BadRequest)
		if strings.Contains(w.Body.String(), name) && name != "" {
			t.Fatalf("invalid name was echoed: %s", w.Body.String())
		}
	}
}

func TestCredentialMutationsHTTPMemberTransports(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialBearer, capability.CredentialSession, capability.CredentialPeer} {
		s, cipher := credentialMutationStore(t)
		b := authorityMember()
		p := &b.snapshot.Authorities[0].Principal
		p.Class = class
		if class == capability.CredentialSession {
			p.Authority.Kind = capability.AuthoritySession
		} else if class == capability.CredentialPeer {
			p.Authority = capability.AuthorityRef{}
			p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
		}
		h := mutationServer(s, cipher, b)
		w := authorityHTTP(h, b, "POST", "/api/v1/secrets", `{"name":"KEY","value":"default-masked"}`)
		if w.Code != 204 {
			t.Fatalf("class %s HTTP %d %s", class, w.Code, w.Body.String())
		}
		tenant, err := s.ForTeam(t.Context(), "team")
		if err != nil {
			t.Fatal(err)
		}
		row, err := tenant.GetSecretRow("KEY", "")
		if err != nil || !row.Masked || row.Shared || row.Principal != p.Capability.Subject.ID {
			t.Fatalf("class %s stored %+v %v", class, row, err)
		}
		w = authorityHTTP(h, b, "DELETE", "/api/v1/secrets/KEY", "")
		if w.Code != 204 {
			t.Fatalf("class %s HTTP %d %s", class, w.Code, w.Body.String())
		}
	}
}

type mutationSealFailure struct{ err error }

func (c mutationSealFailure) Seal(string) (string, error) { return "", c.err }
func (c mutationSealFailure) Open(string) (string, error) { return "", c.err }
func (c mutationSealFailure) SealBound(string, string, string, bool, bool, string) (string, error) {
	return "", c.err
}

func (c mutationSealFailure) OpenBound(string, string, string, bool, bool, string) (string, error) {
	return "", c.err
}

func TestCredentialMutationsHTTPEncryptionFailuresStoreNothing(t *testing.T) {
	s, _ := credentialMutationStore(t)
	for _, route := range []int{0, 2} {
		for _, failure := range []string{"missing", "key refusal", "private"} {
			b := authorityMember()
			var cipher controller.Cipher
			code := apierr.Unavailable
			switch failure {
			case "key refusal":
				cipher, code = mutationSealFailure{secrets.ErrKeyRefused}, apierr.Conflict
			case "private":
				cipher, code = mutationSealFailure{errors.New("private credential cipher diagnostic")}, apierr.Internal
			}
			request := credentialMutationRequests[route]
			w := authorityHTTP(mutationServer(s, cipher, b), b, request.method, request.path, request.body)
			assertEngineError(t, w, apierr.Status(code), code)
		}
	}
	tenant, err := s.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if row, err := tenant.GetSecretRow("KEY", ""); !errors.Is(err, store.ErrNotFound) || row != nil {
		t.Fatalf("stored failed secret: %+v %v", row, err)
	}
	if row, err := tenant.GitCredentialForHost(t.Context(), "git.invalid"); !errors.Is(err, store.ErrNotFound) || row.ID != "" {
		t.Fatalf("stored failed credential: %+v %v", row, err)
	}
}
