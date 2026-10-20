package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCredentialInventoryHTTPUsesAuthorizedTenant(t *testing.T) {
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{31}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range []store.Team{"team", "foreign"} {
		teststore.SeedTeam(t, s, team)
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		for _, masked := range []bool{false, true} {
			name := "CONFIG"
			if masked {
				name = "SECRET"
			}
			value, err := cipher.SealBound(secrets.PurposeSecret, string(team), name, "build", false, masked, string(team)+"-"+name)
			if err != nil {
				t.Fatal(err)
			}
			if err := teststore.SeedSecret(s, tenant.Team(), store.Secret{Name: name, Value: value, Pipeline: "build", Masked: masked}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tenant.WriteGitCredential(t.Context(), credentialSeedAuthority(t, s, team), teststore.SealGitCredential(t, tenant.Team(), store.GitCredential{Host: string(team) + ".invalid", Kind: store.GitCredentialHTTPS, Secret: string(team) + "-git-secret"})); err != nil {
			t.Fatal(err)
		}
	}
	b := authorityMember()
	h := controller.New(s, slog.New(slog.NewTextHandler(io.Discard, nil))).WithSecretsCipher(cipher).WithAuthorization(consoleAuthorization(b)).Handler()
	for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
		w := authorityHTTP(h, b, http.MethodGet, path+"?team=foreign", "")
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "foreign") || strings.Contains(w.Body.String(), "team-SECRET") || strings.Contains(w.Body.String(), "git-secret") {
			t.Fatalf("%s HTTP %d %s", path, w.Code, w.Body.String())
		}
		if path == "/api/v1/secrets" {
			var result struct {
				Secrets []struct {
					Name, Value string
					Masked      bool
				}
			}
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Secrets) != 2 {
				t.Fatalf("secrets=%s", w.Body.String())
			}
			for _, secret := range result.Secrets {
				if secret.Masked && secret.Value != "" || !secret.Masked && secret.Value != "team-CONFIG" {
					t.Fatalf("secret=%+v", secret)
				}
			}
		} else if !strings.Contains(w.Body.String(), `"host":"team.invalid"`) {
			t.Fatalf("git metadata=%s", w.Body.String())
		}
	}
}

type credentialInventory struct {
	team       store.Team
	secrets    []store.Secret
	git        []store.GitCredential
	err        error
	reads      int
	resolves   int
	resolveErr error
	nilReader  bool
}

func (f *credentialInventory) Team() store.Team { return f.team }

func (f *credentialInventory) ListSecrets() ([]store.Secret, error) {
	f.reads++
	return f.secrets, f.err
}

func (f *credentialInventory) GitCredentials(context.Context) ([]store.GitCredential, error) {
	f.reads++
	return f.git, f.err
}

func inventoryServer(t testing.TB, b *credentialBoundary, f *credentialInventory) *controller.Server {
	t.Helper()
	return bindTenantPort[controller.CredentialInventoryReader](controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)),
		func(_ context.Context, team store.Team) (controller.CredentialInventoryReader, error) {
			f.resolves++
			if team != "team" {
				t.Fatalf("resolved foreign tenant %q", team)
			}
			if f.nilReader {
				return nil, f.resolveErr
			}
			return f, f.resolveErr
		})
}

func TestCredentialInventoryHTTPNeverExposesPrivateValues(t *testing.T) {
	property := func(value, selector string) bool {
		b := authorityMember()
		f := &credentialInventory{
			team:    "team",
			secrets: []store.Secret{{Name: "SECRET", Value: value, Masked: true, Pipeline: "build", Principal: "creator"}},
			git:     []store.GitCredential{{ID: value, Team: store.Team(value), Secret: value, Host: "git.invalid", Kind: store.GitCredentialHTTPS}},
		}
		h := inventoryServer(t, b, f).Handler()
		for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
			query := url.Values{"team": {selector}, "run": {selector}, "pipeline": {selector}}
			w := authorityHTTP(h, b, "GET", path+"?"+query.Encode(), "")
			var body map[string][]map[string]json.RawMessage
			key := "credentials"
			if path == "/api/v1/secrets" {
				key = "secrets"
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 1 || len(body[key]) != 1 || w.Header().Get("Cache-Control") != "no-store" {
				return false
			}
			row := body[key][0]
			allowed := []string{"host", "kind", "confirmed", "created_by", "created_at", "updated_at"}
			if key == "secrets" {
				allowed = []string{"name", "principal", "pipeline", "masked", "bound", "created_at", "updated_at"}
				if string(row["name"]) != `"SECRET"` || string(row["masked"]) != "true" {
					return false
				}
			} else if string(row["host"]) != `"git.invalid"` {
				return false
			}
			for field := range row {
				if !slices.Contains(allowed, field) {
					return false
				}
			}
		}
		return f.resolves == 2 && f.reads == 2
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialInventoryHTTPRefusesUnrelatedAuthority(t *testing.T) {
	for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
		for _, identity := range []string{"missing", "revoked", "expired", "membership", "grant", "machine", "work", "trigger", "bootstrap", "audience", "binding"} {
			t.Run(path+"/"+identity, func(t *testing.T) {
				b := authorityMember()
				p := &b.snapshot.Authorities[0].Principal
				code := apierr.Forbidden
				switch identity {
				case "missing":
					b.raw, code = "", apierr.Unauthenticated
				case "revoked":
					p.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "expired":
					p.Capability.ExpiresAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "membership":
					b.snapshot.Authorities[0].MembershipValid, code = false, apierr.Unauthenticated
				case "grant":
					p.Capability.Grants = nil
				case "machine":
					p.Capability.Subject.Kind = capability.SubjectMachine
				case "work", "trigger", "bootstrap":
					b = newCredentialBoundary()
					if identity == "trigger" {
						b = ownerBoundary()
					}
					p = &b.snapshot.Authorities[0].Principal
					p.Capability.Grants = []capability.Grant{capability.RunsRead, capability.TeamAdmin}
					if identity == "bootstrap" {
						p.Capability.Delivery = capability.DeliveryBootstrap
					}
				case "audience":
					b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
					p.Capability.Digest = sha256.Sum256([]byte(b.raw))
					p.Capability.Audience = capability.NodeHost
					p.Capability.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 4, Attempt: 2}
					b.snapshot.Binding.Current, b.snapshot.Binding.ProcessAlive = p.Capability.Binding, true
				case "binding":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "outside team"}
				}
				f := &credentialInventory{team: "team"}
				w := authorityHTTP(inventoryServer(t, b, f).Handler(), b, "GET", path, "")
				assertEngineError(t, w, apierr.Status(code), code)
				if f.resolves != 0 || f.reads != 0 {
					t.Fatal("refused authority reached inventory")
				}
			})
		}
	}
}

func TestCredentialInventoryHTTPReadersAndEmptyArrays(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialBearer, capability.CredentialSession, capability.CredentialPeer} {
		for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
			b := authorityMember()
			p := &b.snapshot.Authorities[0].Principal
			p.Class = class
			p.Capability.Grants = []capability.Grant{capability.RunsRead}
			if class == capability.CredentialSession {
				p.Authority = sessionReference(*p)
			} else if class == capability.CredentialPeer {
				p.Authority = capability.AuthorityRef{}
				p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			}
			f := &credentialInventory{team: "team"}
			w := discoveryRequest(inventoryServer(t, b, f).Handler(), b, "GET", path)
			var body map[string][]json.RawMessage
			key := "credentials"
			if path == "/api/v1/secrets" {
				key = "secrets"
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body[key] == nil || len(body[key]) != 0 || f.reads != 1 {
				t.Fatalf("class %s HTTP %d %s reads=%d", class, w.Code, w.Body.String(), f.reads)
			}
		}
	}
}

func TestCredentialInventoryHTTPGrantsAreRouteSpecific(t *testing.T) {
	for _, grant := range []capability.Grant{capability.RunsRead, capability.TeamAdmin} {
		for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
			b := authorityMember()
			b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{grant}
			f := &credentialInventory{team: "team"}
			w := authorityHTTP(inventoryServer(t, b, f).Handler(), b, "GET", path, "")
			if grant == capability.TeamAdmin && path == "/api/v1/team/git-credentials" {
				assertEngineError(t, w, 403, apierr.Forbidden)
				if f.reads != 0 || f.resolves != 0 {
					t.Fatal("missing inventory grant reached persistence")
				}
			} else if w.Code != 200 || f.reads != 1 {
				t.Fatalf("grant %s HTTP %d %s reads=%d", grant, w.Code, w.Body.String(), f.reads)
			}
		}
	}
}

func TestCredentialInventoryHTTPFailuresArePrivate(t *testing.T) {
	for _, path := range []string{"/api/v1/secrets", "/api/v1/team/git-credentials"} {
		for _, failure := range []string{"reader", "resolver", "nil", "foreign", "unknown team", "head", "authenticator", "missing dependency"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				b, f := authorityMember(), &credentialInventory{team: "team"}
				code, method, reads := apierr.Internal, "GET", 0
				switch failure {
				case "reader":
					f.err, reads = errors.New("private credential"), 1
				case "resolver":
					f.resolveErr = errors.New("private resolver credential")
				case "nil":
					f.nilReader, code = true, apierr.PolicyMissing
				case "foreign":
					f.team, code = "foreign", apierr.PolicyMissing
				case "unknown team":
					f.resolveErr, code = store.ErrUnknownTeam, apierr.NotFound
				case "head":
					method, code = "HEAD", apierr.MethodNotAllowed
				case "authenticator":
					b.readErr = errors.New("private authentication credential")
				case "missing dependency":
					code = apierr.PolicyMissing
				}
				s := inventoryServer(t, b, f)
				if failure == "missing dependency" {
					bindTenantPort[controller.CredentialInventoryReader](s, nil)
				}
				w := authorityHTTP(s.Handler(), b, method, path, "")
				assertEngineError(t, w, apierr.Status(code), code)
				if strings.Contains(w.Body.String(), "private") || f.reads != reads {
					t.Fatalf("HTTP %d %s reads=%d", w.Code, w.Body.String(), f.reads)
				}
			})
		}
	}
}

func TestCredentialInventoryHTTPConfigFailureReturnsNoPartialList(t *testing.T) {
	for _, configured := range []bool{false, true} {
		b := authorityMember()
		cipher, err := secrets.NewCipher(bytes.Repeat([]byte{32}, secrets.KeySize))
		if err != nil {
			t.Fatal(err)
		}
		value, err := cipher.SealBound(secrets.PurposeSecret, "team", "CONFIG", "build", false, false, "partial-config")
		if err != nil {
			t.Fatal(err)
		}
		f := &credentialInventory{team: "team", secrets: []store.Secret{{Name: "CONFIG", Pipeline: "build", Value: value}, {Name: "BROKEN", Value: "private envelope"}}}
		s := inventoryServer(t, b, f)
		code := apierr.Unavailable
		if configured {
			s.WithSecretsCipher(cipher)
			code = apierr.Internal
		}
		w := authorityHTTP(s.Handler(), b, "GET", "/api/v1/secrets", "")
		assertEngineError(t, w, apierr.Status(code), code)
		if strings.Contains(w.Body.String(), "partial-config") || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("partial list: %s", w.Body.String())
		}
	}
}

func TestCredentialInventoryHTTPRejectsChangedSecretBinding(t *testing.T) {
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{33}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	property := func(value string, field uint8) bool {
		team, name, pipeline, shared, masked := "team", "CONFIG", "build", false, false
		row := store.Secret{Name: name, Pipeline: pipeline}
		switch field % 5 {
		case 0:
			team = "foreign"
		case 1:
			row.Name = "OTHER"
		case 2:
			row.Pipeline = "other"
		case 3:
			row.Shared = true
		case 4:
			masked = true
		}
		var err error
		row.Value, err = cipher.SealBound(secrets.PurposeSecret, team, name, pipeline, shared, masked, value)
		if err != nil {
			t.Fatal(err)
		}
		b := authorityMember()
		f := &credentialInventory{team: "team", secrets: []store.Secret{row}}
		w := authorityHTTP(inventoryServer(t, b, f).WithSecretsCipher(cipher).Handler(), b, "GET", "/api/v1/secrets", "")
		var result apierr.Envelope
		return w.Code == 500 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error.Code == apierr.Internal && result.Error.Message == "request failed"
	}
	for field := range uint8(5) {
		if !property("secret", field) {
			t.Fatalf("accepted changed binding field %d", field)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
