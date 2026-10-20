package acceptance_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/jwks"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type oidcFacts struct {
	selectorTransactions
	facts capability.OIDCFacts
	actor capability.Authorized
	calls int
	err   error
}

func (*oidcFacts) Team() store.Team { return "team" }
func (f *oidcFacts) ReadOIDCFacts(_ context.Context, a capability.Authorized) (capability.OIDCFacts, error) {
	f.calls++
	f.actor = a
	return f.facts, f.err
}

func oidcBoundary() (*credentialBoundary, *oidcFacts) {
	b := newCredentialBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.OIDCMint)
	f := &oidcFacts{selectorTransactions: selectorTransactions{team: "team"}, facts: capability.OIDCFacts{
		Now: b.snapshot.Now, NotAfter: b.snapshot.Now.Add(5 * time.Minute),
		Team: "team", RunID: "run", Pipeline: "deploy", Trigger: "push", RunnerKind: "runner", Ref: "refs/heads/main",
		SHA: strings.Repeat("a", 40), Repository: "github.com/acme/api", RepositoryID: 42,
	}}
	return b, f
}

func oidcPrivate(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func oidcServer(t *testing.T, b *credentialBoundary, f *oidcFacts, private, published []byte) *controller.Server {
	t.Helper()
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithEngineController(&controller.EngineController{
		ResolveTenant: selectorTenant("team", nil),
	})
	if private != nil {
		issuer, err := oidcissuer.New("https://issuer.example", private, published, 0)
		if err != nil {
			t.Fatal(err)
		}
		s.WithOIDCIssuer(issuer)
	}
	if f != nil {
		bindTenantPort[controller.OIDCReader](s, func(context.Context, store.Team) (controller.OIDCReader, error) { return f, nil })
	}
	return s
}

func oidcResponse(t *testing.T, w *httptest.ResponseRecorder, key *rsa.PublicKey) (controller.OIDCTokenResponse, map[string]any) {
	t.Helper()
	var result controller.OIDCTokenResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	parts := strings.Split(result.Token, ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := jwks.DecodeSegment(parts[1], &claims); err != nil {
		t.Fatal(err)
	}
	return result, claims
}

func TestOIDCMintHTTPIdentityAndExpiryProperty(t *testing.T) {
	b, f := oidcBoundary()
	key, private := oidcPrivate(t)
	server := oidcServer(t, b, f, private, nil)
	h := server.Handler()
	property := func(input []byte, seconds uint16) bool {
		f.calls = 0
		forged := hex.EncodeToString(input)
		f.facts.NotAfter = b.snapshot.Now.Add(time.Duration(1+seconds%600) * time.Second)
		expiry := b.snapshot.Now.Add(time.Duration(1+(seconds/2)%600) * time.Second)
		deadline := b.snapshot.Now.Add(time.Duration(1+(seconds/3)%600) * time.Second)
		revocation := b.snapshot.Now.Add(time.Duration(1+(seconds/4)%600) * time.Second)
		b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &revocation
		b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &expiry
		b.snapshot.Authorities[0].Principal.Capability.Binding.Deadline = &deadline
		b.snapshot.Binding.Current = b.snapshot.Authorities[0].Principal.Capability.Binding
		b.snapshot.Authorities[0].Principal.Authority.ID = "claim-" + forged
		b.snapshot.Authorities[0].Principal.Capability.Prefix = "claim-" + forged
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token?team="+forged+"&run_id="+forged+"&repository="+forged, `{"audience":"vault"}`)
		result, c := oidcResponse(t, w, &key.PublicKey)
		ceiling := min(f.facts.NotAfter.Unix(), expiry.Unix(), deadline.Unix(), revocation.Unix())
		return f.calls == 1 && f.actor.Target.Team == "team" && f.actor.Target.RunID == "run" && f.actor.Access.Binding == capability.BindRun &&
			f.actor.Principal.Authority == b.snapshot.Authorities[0].Principal.Authority && f.actor.Binding.Generation == 4 &&
			c["iss"] == "https://issuer.example" && c["aud"] == "vault" && c["sub"] == "team:team:repository_id:42:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main" &&
			c["team"] == "team" && c["run_id"] == "run" && c["repository"] == f.facts.Repository && c["sha"] == f.facts.SHA &&
			c["iat"] == float64(f.facts.Now.Unix()) && c["nbf"] == c["iat"] && c["exp"] == float64(ceiling) && result.ExpiresAt.Unix() == ceiling &&
			w.Header().Get("Cache-Control") == "no-store" && !strings.Contains(w.Body.String(), b.raw)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCMintHTTPRefusals(t *testing.T) {
	_, private := oidcPrivate(t)
	for _, state := range []string{"anonymous", "machine", "member", "session", "console", "peer", "bootstrap", "grant", "revoked", "ancestor", "expired", "cancelled", "ended", "binding", "backend", "transaction", "race", "provider", "private", "tenant", "run", "clock", "ceiling", "subject", "missing reader", "disabled"} {
		t.Run(state, func(t *testing.T) {
			b, f := oidcBoundary()
			p := &b.snapshot.Authorities[0].Principal
			want, calls := apierr.Forbidden, 0
			switch state {
			case "anonymous":
				b.raw = ""
				want = apierr.Unauthenticated
			case "machine":
				b = authorityMember()
				b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
			case "member":
				b = authorityMember()
			case "session":
				b = issuanceSession()
			case "console":
				b = consoleBoundary()
				want = apierr.NotFound
			case "peer":
				b = authorityMember()
				p = &b.snapshot.Authorities[0].Principal
				p.Class = capability.CredentialPeer
				p.Authority = capability.AuthorityRef{}
				p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			case "bootstrap":
				p.Capability.Delivery = capability.DeliveryBootstrap
				p.Capability.Grants = []capability.Grant{capability.SourceRead, capability.ClaimExchange}
			case "grant":
				p.Capability.Grants = nil
			case "revoked":
				p.Capability.RevokedAt = &b.snapshot.Now
				want = apierr.Unauthenticated
			case "ancestor":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				want = apierr.Unauthenticated
			case "expired":
				p.Capability.ExpiresAt = &b.snapshot.Now
				want = apierr.Unauthenticated
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
				want = apierr.Unauthenticated
			case "ended":
				b.snapshot.Binding.RunFinished = true
				want = apierr.Unauthenticated
			case "binding":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "wrong run"}
			case "backend":
				b.readErr = &apierr.Error{Code: apierr.Unavailable, Message: "unavailable"}
				want = apierr.Unavailable
			case "transaction":
				f.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "claim ended"}
				want, calls = apierr.Unauthenticated, 1
			case "race":
				f.err = &apierr.Error{Code: apierr.LockHeld, Message: "claim fence changed"}
				want, calls = apierr.LockHeld, 1
			case "provider":
				f.err = &apierr.Error{Code: apierr.Unavailable, Message: "identity unavailable"}
				want, calls = apierr.Unavailable, 1
			case "private":
				f.err = errors.New("private-db-detail")
				want, calls = apierr.Internal, 1
			case "tenant":
				f.facts.Team = "foreign"
				want, calls = apierr.Internal, 1
			case "run":
				f.facts.RunID = "foreign"
				want, calls = apierr.Internal, 1
			case "clock":
				f.facts.Now = time.Time{}
				want, calls = apierr.Internal, 1
			case "ceiling":
				f.facts.NotAfter = f.facts.Now
				want, calls = apierr.Internal, 1
			case "subject":
				f.facts.Pipeline = "private:trigger:push"
				want, calls = apierr.BadRequest, 1
			case "missing reader":
				want = apierr.PolicyMissing
			case "disabled":
				want = apierr.NotFound
			}
			signing := private
			reader := f
			if state == "missing reader" {
				reader = nil
			}
			if state == "disabled" {
				signing = nil
			}
			h := oidcServer(t, b, reader, signing, nil).Handler()
			w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
			assertEngineError(t, w, apierr.Status(want), want)
			if f.calls != calls || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("calls=%d body=%s", f.calls, w.Body.String())
			}
		})
	}
}

func TestOIDCMintHTTPInputAndBudget(t *testing.T) {
	b, f := oidcBoundary()
	_, private := oidcPrivate(t)
	server := oidcServer(t, b, f, private, nil)
	h := server.Handler()
	for _, body := range []string{`null`, `{}`, `{"audience":"two words"}`, `{"audience":"` + strings.Repeat("a", 257) + `"}`, `{"audience":"vault","team":"foreign"}`, `{"audience":"vault"} {}`} {
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", body)
		assertEngineError(t, w, 400, apierr.BadRequest)
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached facts reader")
	}
	for _, method := range []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS"} {
		w := authorityHTTP(h, b, method, "/api/v1/runs/run/oidc-token", "")
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
	var previous string
	for i := 0; i < 30; i++ {
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
		var result controller.OIDCTokenResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == previous {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body.String())
		}
		previous = result.Token
	}
	w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
	assertEngineError(t, w, 429, apierr.RateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no retry delay")
	}
	b.snapshot.Authorities[0].Principal.Authority.ID = "second-claim"
	b.snapshot.Authorities[0].Principal.Capability.Prefix = "second-claim"
	if w = authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`); w.Code != 200 {
		t.Fatalf("claim shares another claim's budget: %d", w.Code)
	}
}

func TestOIDCMintHTTPKeyRotation(t *testing.T) {
	b, f := oidcBoundary()
	_, oldKey := oidcPrivate(t)
	_, nextKey := oidcPrivate(t)
	servers := []http.Handler{
		oidcServer(t, b, f, oldKey, nil).Handler(), oidcServer(t, b, f, oldKey, nextKey).Handler(),
		oidcServer(t, b, f, nextKey, oldKey).Handler(), oidcServer(t, b, f, nextKey, nil).Handler(),
	}
	minted := make([]string, len(servers))
	for i, h := range servers {
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
		var result controller.OIDCTokenResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
			t.Fatalf("mint: %d %s", w.Code, w.Body)
		}
		minted[i] = result.Token
	}
	for i, h := range servers {
		verifier := jwks.New("https://issuer.example"+oidcissuer.JWKSPath, &http.Client{Transport: reportTransport{handler: h}}, nil)
		for j, token := range minted {
			var claims map[string]any
			err := verifier.Verify(t.Context(), token, &claims)
			accepted := (i != 0 || j < 2) && (i != 3 || j >= 2)
			if (err == nil) != accepted {
				t.Fatalf("key set %d / token %d: %v", i, j, err)
			}
		}
	}
	parts := strings.Split(minted[0], ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"team":"forged"}`))
	verifier := jwks.New("https://issuer.example"+oidcissuer.JWKSPath, &http.Client{Transport: reportTransport{handler: servers[0]}}, nil)
	var claims map[string]any
	if err := verifier.Verify(t.Context(), strings.Join(parts, "."), &claims); !errors.Is(err, jwks.ErrRejected) {
		t.Fatalf("tampered JWT: %v", err)
	}
}

func TestOIDCMintHTTPCommittedSubjects(t *testing.T) {
	key, private := oidcPrivate(t)
	for _, tc := range []struct {
		event, ref, kind string
		repositoryID     int64
	}{
		{"push", "refs/heads/main", "runner", 42},
		{"pull_request", "refs/pull/7/head", "runner", 42},
		{"push", "refs/tags/v1.2.3", "runner", 42},
		{"release", "refs/tags/v1", "runner", 42},
		{"create", "refs/heads/topic", "runner", 42},
		{"delete", "refs/heads/topic", "runner", 42},
		{"cron", "refs/heads/main", "service", 0},
		{"manual", "", "user", 0},
		{"push", "refs/heads/main", "github-actions", 42},
		{"push", "refs/heads/main", "runner", 9001},
	} {
		b, f := oidcBoundary()
		f.facts.Trigger, f.facts.Ref, f.facts.RunnerKind, f.facts.RepositoryID = tc.event, tc.ref, tc.kind, tc.repositoryID
		h := oidcServer(t, b, f, private, nil).Handler()
		w := authorityHTTP(h, b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
		_, claims := oidcResponse(t, w, &key.PublicKey)
		expected := oidcissuer.Claims{Team: "team", Pipeline: "deploy", Trigger: tc.event, RunnerKind: tc.kind, Ref: tc.ref, RepositoryID: tc.repositoryID}
		subject, err := expected.Subject()
		if err != nil || claims["sub"] != subject || claims["trigger"] != tc.event || claims["runner_kind"] != tc.kind || (tc.ref != "" && claims["ref"] != tc.ref) {
			t.Fatalf("%+v: %v", tc, claims)
		}
	}
}

func TestOIDCMintHTTPSubjectInjectionProperty(t *testing.T) {
	_, private := oidcPrivate(t)
	property := func(input []byte, field, invalid uint8) bool {
		b, f := oidcBoundary()
		value := "private-subject-" + hex.EncodeToString(input) + []string{":trigger:push", "*", "?", " ", "\x00", "é"}[invalid%6]
		fields := []*string{&f.facts.Pipeline, &f.facts.Trigger, &f.facts.RunnerKind, &f.facts.Ref}
		*fields[field%uint8(len(fields))] = value
		w := authorityHTTP(oidcServer(t, b, f, private, nil).Handler(), b, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`)
		var result apierr.Envelope
		return w.Code == 400 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && result.Error.Code == apierr.BadRequest &&
			!strings.Contains(w.Body.String(), "token") && !strings.Contains(w.Body.String(), value)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
