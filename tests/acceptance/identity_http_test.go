package acceptance_test

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
)

func identityServer(b *credentialBoundary) http.Handler {
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
}

func TestIdentityHTTPUsesCommittedPrincipal(t *testing.T) {
	for _, kind := range []string{"member", "machine", "claim", "deployment", "session", "console", "peer", "no grants"} {
		t.Run(kind, func(t *testing.T) {
			b := authorityMember()
			switch kind {
			case "machine":
				b.snapshot.Authorities[0].Principal.Capability.Subject = capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}
			case "claim":
				b = newCredentialBoundary()
			case "deployment":
				b = deploymentBoundary(capability.OperatorTokens)
			case "session":
				b.snapshot.Authorities[0].Principal.Class = capability.CredentialSession
				b.snapshot.Authorities[0].Principal.Authority = sessionReference(b.snapshot.Authorities[0].Principal)
			case "console":
				b = consoleBoundary()
			case "peer":
				p := &b.snapshot.Authorities[0].Principal
				p.Class, p.Authority = capability.CredentialPeer, capability.AuthorityRef{}
				p.Capability.Prefix = ""
				p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			case "no grants":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
			}
			p := b.snapshot.Authorities[0].Principal
			r := httptest.NewRequest("GET", "/api/v1/auth/whoami?team=forged&principal=forged", nil)
			if p.Class == capability.CredentialBearer {
				r.Header.Set("Authorization", "Bearer "+b.raw)
			} else {
				r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
			}
			w := httptest.NewRecorder()
			identityServer(b).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			var got struct {
				Principal   string             `json:"principal"`
				Kind        string             `json:"kind"`
				Team        string             `json:"team"`
				TokenPrefix string             `json:"token_prefix"`
				Scopes      []capability.Grant `json:"scopes"`
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Principal != p.Capability.Subject.ID || got.Kind != string(p.Capability.Subject.Kind) || got.Team != p.Capability.Team || !slices.Equal(got.Scopes, p.Capability.Grants) {
				t.Fatalf("identity = %s", w.Body.String())
			}
			if got.Scopes == nil {
				t.Fatal("empty grants must be an explicit array")
			}
			if p.Class == capability.CredentialBearer && got.TokenPrefix != p.Capability.Prefix || p.Class != capability.CredentialBearer && got.TokenPrefix != "" {
				t.Fatalf("credential prefix = %q", got.TokenPrefix)
			}
			for key := range body {
				if !slices.Contains([]string{"principal", "kind", "team", "scopes", "token_prefix"}, key) {
					t.Fatalf("private field %s in %s", key, w.Body.String())
				}
			}
			if strings.Contains(w.Body.String(), b.raw) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unsafe identity response: %v %s", w.Header(), w.Body.String())
			}
		})
	}
}

func TestIdentityHTTPRejectsBootstrapAndEval(t *testing.T) {
	for _, name := range []string{"bootstrap", "eval"} {
		b := newCredentialBoundary()
		p := &b.snapshot.Authorities[0].Principal
		if name == "bootstrap" {
			p.Capability.Delivery, p.Capability.Grants = capability.DeliveryBootstrap, capability.BootstrapGrants()
		} else {
			p.Capability.Unit, p.Capability.Purpose = capability.UnitEval, capability.PurposeEval
			p.Capability.Grants = []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
			p.Capability.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "unit", Holder: "eval-host", Generation: 1}
			expires := b.snapshot.Now.Add(time.Minute)
			b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			b.snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *p.Capability.Binding.Expansion, ExpiresAt: expires}
		}
		w := discoveryRequest(identityServer(b), b, "GET", "/api/v1/auth/whoami")
		assertEngineError(t, w, 403, apierr.Forbidden)
	}
}

func TestIdentityHTTPDoesNotTrustLocalHeaders(t *testing.T) {
	b := authorityMember()
	h := identityServer(b)
	for _, header := range []string{"X-User", "X-Uid", "X-Sparkwing-Principal", "X-Forwarded-User"} {
		r := httptest.NewRequest("GET", "http://localhost/api/v1/auth/whoami", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set(header, "uid:1000")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertEngineError(t, w, 401, apierr.Unauthenticated)
	}
}

func TestIdentityHTTPRefusesInvalidCredentials(t *testing.T) {
	for _, state := range []string{"absent", "malformed", "unknown", "revoked", "expired", "parent revoked", "membership removed", "wrong audience", "private backend error", "missing policy"} {
		t.Run(state, func(t *testing.T) {
			b := newCredentialBoundary()
			code := apierr.Unauthenticated
			switch state {
			case "absent":
				b.raw = ""
			case "malformed":
				b.raw = "bad"
			case "unknown":
				b.raw = "swk_" + strings.Repeat("A", 43)
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "expired":
				b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
			case "parent revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "membership removed":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "wrong audience":
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
				b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
				b.snapshot.Binding.ProcessAlive = true
				code = apierr.Forbidden
			case "private backend error":
				b.readErr, code = io.ErrUnexpectedEOF, apierr.Internal
			case "missing policy":
				code = apierr.PolicyMissing
			}
			var h http.Handler = identityServer(b)
			if state == "missing policy" {
				h = controller.New(nil, nil).Handler()
			}
			r := httptest.NewRequest("GET", "/api/v1/auth/whoami", nil)
			if b.raw != "" {
				r.Header.Set("Authorization", "Bearer "+b.raw)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertEngineError(t, w, apierr.Status(code), code)
		})
	}
}

func TestIdentityHTTPMethods(t *testing.T) {
	for _, method := range []string{"HEAD", "POST", "PATCH", "DELETE", "PUT", "OPTIONS", "TRACE"} {
		w := httptest.NewRecorder()
		controller.New(nil, nil).Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/v1/auth/whoami", nil))
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		if w.Header().Get("Allow") != "GET" {
			t.Fatalf("Allow = %q", w.Header().Get("Allow"))
		}
	}
}
