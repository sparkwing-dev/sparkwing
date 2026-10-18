package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type secretReads struct {
	team       store.Team
	row        *store.Secret
	authorized capability.Authorized
	err        error
	reads      int
}

func (f *secretReads) Team() store.Team { return f.team }

func (f *secretReads) ReadSecret(_ context.Context, a capability.Authorized) (*store.Secret, error) {
	f.reads++
	f.authorized = a
	return f.row, f.err
}

type secretReadCipher struct {
	controller.BoundCipher
	opens int
	err   error
}

func (c *secretReadCipher) OpenBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, value string) (string, error) {
	c.opens++
	if c.err != nil {
		return "", c.err
	}
	return c.BoundCipher.OpenBound(purpose, team, name, scope, shared, masked, value)
}

func secretReadFixture(t testing.TB, b *credentialBoundary, value string) (*secretReads, *secretReadCipher, *controller.Server) {
	t.Helper()
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{37}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	masked := b.snapshot.Authorities[0].Principal.Capability.Binding.Kind != capability.BindingNone
	envelope, err := cipher.SealBound(secrets.PurposeSecret, "team", "TOKEN", "build", false, masked, value)
	if err != nil {
		t.Fatal(err)
	}
	f := &secretReads{team: "team", row: &store.Secret{
		Name: "TOKEN", Value: envelope, Pipeline: "build", Masked: masked, Principal: "creator",
		CreatedAt: b.snapshot.Now, UpdatedAt: b.snapshot.Now,
	}}
	c := &secretReadCipher{BoundCipher: cipher}
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithSecretsCipher(c).
		WithSecretReader(func(_ context.Context, team store.Team) (controller.SecretReader, error) {
			if team != "team" {
				t.Fatalf("read selected tenant %q", team)
			}
			return f, nil
		})
	return f, c, s
}

func secretClaim() *credentialBoundary {
	b := newCredentialBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.SecretsRelease)
	return b
}

func TestSecretReadHTTPUsesPolicyAndCommittedRow(t *testing.T) {
	for _, kind := range []string{"claim", "trigger", "plan", "member", "reader", "session", "peer", "shared", "implicit run"} {
		t.Run(kind, func(t *testing.T) {
			b := secretClaim()
			if kind == "trigger" {
				p := &b.snapshot.Authorities[0].Principal.Capability
				p.Unit, p.Binding.Kind, p.Binding.NodeID = capability.UnitTrigger, capability.BindingTrigger, ""
				b.snapshot.Binding.Current = p.Binding
			} else if kind == "plan" {
				p := &b.snapshot.Authorities[0].Principal.Capability
				p.Unit, p.Purpose, p.Binding.Kind, p.Binding.NodeID = capability.UnitPlan, capability.PurposePlan, capability.BindingRun, ""
				b.snapshot.Binding.Current = p.Binding
			} else if kind == "member" || kind == "reader" || kind == "session" || kind == "peer" {
				b = authorityMember()
				if kind == "reader" {
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.SecretsRead}
				} else if kind == "session" {
					p := &b.snapshot.Authorities[0].Principal
					p.Class = capability.CredentialSession
					p.Authority = sessionReference(*p)
				} else if kind == "peer" {
					p := &b.snapshot.Authorities[0].Principal
					p.Class = capability.CredentialPeer
					p.Authority = capability.AuthorityRef{}
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:42"}
				}
			}
			f, c, server := secretReadFixture(t, b, "private-value")
			if kind == "shared" {
				f.row.Pipeline, f.row.Shared = "", true
				var err error
				f.row.Value, err = c.SealBound(secrets.PurposeSecret, "team", "TOKEN", "", true, true, "private-value")
				if err != nil {
					t.Fatal(err)
				}
			}
			path, run := "/api/v1/secrets/TOKEN?run=run&pipeline=build&team=foreign", "run"
			if kind == "implicit run" {
				path, run = "/api/v1/secrets/TOKEN?pipeline=build&team=foreign", ""
			}
			w := authorityHTTP(server.Handler(), b, "GET", path, "")
			var body map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["value"] != "private-value" || body["bound"] != true || f.reads != 1 || c.opens != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s reads=%d opens=%d", w.Code, w.Body.String(), f.reads, c.opens)
			}
			a := f.authorized
			if a.Target.Team != "team" || a.Target.SecretName != "TOKEN" || a.Target.RunID != run || a.Target.Pipeline != "build" || a.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || a.Binding != b.snapshot.Authorities[0].Principal.Capability.Binding {
				t.Fatalf("authorization=%+v", a)
			}
			if a.Binding.Kind != capability.BindingNone {
				if a.Access.Binding != capability.BindDeclaredSecret {
					t.Fatalf("binding=%s", a.Access.Binding)
				}
			}
		})
	}
}

func TestSecretReadHTTPAuthorityProperty(t *testing.T) {
	property := func(state uint8, value string) bool {
		b := secretClaim()
		f, c, s := secretReadFixture(t, b, value)
		path := "/api/v1/secrets/TOKEN?run=run"
		switch state % 10 {
		case 0:
			b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
		case 1:
			b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
		case 2:
			b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
		case 3:
			b.snapshot.Binding.FinishedAt = &b.snapshot.Now
		case 4:
			b.snapshot.Binding.RunCancelled = true
		case 5:
			b.snapshot.Binding.Current.Holder = "replacement"
		case 6:
			b.snapshot.Binding.Current.Generation++
		case 7:
			b.snapshot.Authorities[0].Principal.Capability.Grants = nil
		case 8:
			path = "/api/v1/secrets/TOKEN?run=foreign"
		case 9:
			b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "undeclared secret", Details: map[string]any{"reason": apierr.ReasonBinding}}
		}
		w := authorityHTTP(s.Handler(), b, "GET", path, "")
		return (w.Code == 401 || w.Code == 403) && f.reads == 0 && c.opens == 0
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSecretReadHTTPRefusalsNeverOpenValues(t *testing.T) {
	for _, state := range []string{"absent", "unknown", "expired", "parent revoked", "cancelled", "finished", "stale holder", "missing grant", "root", "bootstrap", "foreign run", "undeclared", "pool", "transaction", "missing row", "masked member", "foreign reader", "wrong row", "cipher", "no cipher", "unbound cipher", "malformed selector", "duplicate run", "invalid name", "head"} {
		t.Run(state, func(t *testing.T) {
			b := secretClaim()
			if state == "masked member" {
				b = authorityMember()
			}
			f, c, server := secretReadFixture(t, b, "private-value")
			path, method := "/api/v1/secrets/TOKEN?run=run", "GET"
			code, reads, opens := apierr.Unauthenticated, 0, 0
			switch state {
			case "absent":
				b.raw = ""
			case "unknown":
				b.raw += "unknown"
			case "expired":
				b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
			case "parent revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
				code = apierr.Forbidden
			case "finished":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "stale holder":
				b.snapshot.Binding.Current.Holder = "replacement"
			case "missing grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				code = apierr.Forbidden
			case "root":
				p := &b.snapshot.Authorities[0].Principal.Capability
				p.Binding, p.Unit, p.Purpose = capability.Binding{Kind: capability.BindingNone}, "", ""
				p.Issuance, p.Parent = capability.Delegate, nil
				b.snapshot.Authorities = b.snapshot.Authorities[:1]
				code = apierr.Forbidden
			case "bootstrap":
				b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.SourceRead, capability.ClaimExchange}
				code = apierr.Forbidden
			case "foreign run":
				path, code = "/api/v1/secrets/TOKEN?run=foreign", apierr.Forbidden
			case "undeclared", "pool":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "secret binding refused", Details: map[string]any{"reason": apierr.ReasonBinding}}
				code = apierr.Forbidden
			case "transaction":
				f.err, code, reads = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}, apierr.LockHeld, 1
			case "missing row":
				f.err, code, reads = store.ErrNotFound, apierr.NotFound, 1
			case "masked member":
				f.err, code, reads = &apierr.Error{Code: apierr.Forbidden, Message: "masked secret is write-only", Details: map[string]any{"reason": apierr.ReasonBinding}}, apierr.Forbidden, 1
			case "foreign reader":
				f.team, code = "foreign", apierr.PolicyMissing
			case "wrong row":
				f.row.Name, code, reads = "FOREIGN", apierr.Internal, 1
			case "cipher":
				c.err = &apierr.Error{Code: apierr.Forbidden, Message: "private-value"}
				code, reads, opens = apierr.Internal, 1, 1
			case "no cipher":
				server.WithSecretsCipher(nil)
				code = apierr.Unavailable
			case "unbound cipher":
				server.WithSecretsCipher(plainCipher{})
				code = apierr.Unavailable
			case "malformed selector":
				path, code = "/api/v1/secrets/TOKEN?run=%zz", apierr.BadRequest
			case "duplicate run":
				path, code = "/api/v1/secrets/TOKEN?run=run&run=foreign", apierr.BadRequest
			case "invalid name":
				path, code = "/api/v1/secrets/bad%20name", apierr.BadRequest
			case "head":
				method, code = "HEAD", apierr.MethodNotAllowed
			}
			w := authorityHTTP(server.Handler(), b, method, path, "")
			assertEngineError(t, w, apierr.Status(code), code)
			if f.reads != reads || c.opens != opens || bytes.Contains(w.Body.Bytes(), []byte("private-value")) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("reads=%d opens=%d HTTP %d %s", f.reads, c.opens, w.Code, w.Body.String())
			}
		})
	}
}

func TestSecretReadHTTPEphemeralValueSkipsDecryption(t *testing.T) {
	b := secretClaim()
	f, c, server := secretReadFixture(t, b, "private-value")
	f.row.Ephemeral, f.row.Value = true, "private-value"
	c.err = errors.New("ephemeral values cannot be decrypted")
	w := authorityHTTP(server.Handler(), b, "GET", "/api/v1/secrets/TOKEN?run=run", "")
	var result controllerwire.SecretValue
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Value != "private-value" || f.reads != 1 || c.opens != 0 {
		t.Fatalf("ephemeral secret: reads=%d opens=%d HTTP %d %s", f.reads, c.opens, w.Code, w.Body.String())
	}
}

func TestSecretReadHTTPBindingProperty(t *testing.T) {
	property := func(value, selector string, mutation uint8) bool {
		b := secretClaim()
		f, c, server := secretReadFixture(t, b, value)
		switch mutation % 11 {
		case 0:
			f.team = "foreign"
		case 1:
			f.row.Name = "OTHER"
		case 2:
			f.row.Pipeline = "foreign"
		case 3:
			f.row.Shared = true
		case 4:
			f.row.Masked = false
		case 5:
			b = authorityMember()
			f.row.Masked = true
			server.WithAuthorization(consoleAuthorization(b))
		case 6:
			f.err = errors.New(value)
		case 7:
			f.row.Value = "unbound:" + value
		case 8:
			var err error
			f.row.Value, err = c.SealBound(secrets.PurposeSecret, "foreign", "TOKEN", "build", false, true, value)
			if err != nil {
				t.Fatal(err)
			}
		case 9:
			server.WithSecretsCipher(nil)
		case 10:
			server.WithSecretsCipher(plainCipher{})
		}
		query := url.Values{"team": {selector}, "pipeline": {selector}, "run": {"run"}}
		w := authorityHTTP(server.Handler(), b, "GET", "/api/v1/secrets/TOKEN?"+query.Encode(), "")
		var result apierr.Envelope
		if mutation%11 >= 9 {
			return w.Code == 503 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && result.Error.Code == apierr.Unavailable && f.reads == 0 && c.opens == 0
		}
		if w.Code != 500 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error == nil {
			return false
		}
		if mutation%11 == 0 {
			return result.Error.Code == apierr.PolicyMissing && c.opens == 0
		}
		return result.Error.Code == apierr.Internal && result.Error.Message == "request failed" && result.Error.Hint == "" && len(result.Error.Details) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 80}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretReadHTTPRequiresTransactionalReader(t *testing.T) {
	for _, state := range []string{"missing", "nil", "private failure", "real tenant"} {
		t.Run(state, func(t *testing.T) {
			b := secretClaim()
			_, c, s := secretReadFixture(t, b, "private-value")
			code := apierr.PolicyMissing
			s.WithSecretReader(nil)
			switch state {
			case "nil":
				s.WithSecretReader(func(context.Context, store.Team) (controller.SecretReader, error) { return nil, nil })
			case "private failure":
				s.WithSecretReader(func(context.Context, store.Team) (controller.SecretReader, error) {
					return nil, errors.New("private-value")
				})
				code = apierr.Internal
			case "real tenant":
				code = apierr.Unauthenticated
				st, _ := credentialMutationStore(t)
				s = controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).WithSecretsCipher(c).WithAuthorization(consoleAuthorization(b))
			}
			w := authorityHTTP(s.Handler(), b, "GET", "/api/v1/secrets/TOKEN?run=run", "")
			assertEngineError(t, w, apierr.Status(code), code)
			if c.opens != 0 || bytes.Contains(w.Body.Bytes(), []byte("private-value")) {
				t.Fatal("missing transaction reached secret decryption")
			}
		})
	}
}
