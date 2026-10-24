package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type capabilityManagement struct {
	team    store.Team
	rows    []capability.Metadata
	result  capability.Rotation
	err     error
	calls   int
	auth    capability.Authorized
	filter  capability.InventoryFilter
	grace   time.Duration
	expires *time.Time
}

func (f *capabilityManagement) Team() store.Team { return f.team }

func (f *capabilityManagement) ListCapabilities(_ context.Context, a capability.Authorized, filter capability.InventoryFilter) ([]capability.Metadata, error) {
	f.calls++
	f.auth, f.filter = a, filter
	return f.rows, f.err
}

func (f *capabilityManagement) ReadCapability(_ context.Context, a capability.Authorized) (capability.Metadata, error) {
	f.calls++
	f.auth = a
	if len(f.rows) == 0 {
		return capability.Metadata{}, store.ErrNotFound
	}
	return f.rows[0], f.err
}

func (f *capabilityManagement) RotateCapability(_ context.Context, a capability.Authorized, grace time.Duration, expires *time.Time) (capability.Rotation, error) {
	f.calls++
	f.auth, f.grace, f.expires = a, grace, expires
	return f.result, f.err
}

func managementFixture(operator bool) (*credentialBoundary, *capabilityManagement) {
	b := authorityMember()
	if operator {
		b = deploymentBoundary(capability.OperatorTokens)
	}
	p := b.snapshot.Authorities[0].Principal
	row := capability.Metadata{
		Prefix: "target", Team: p.Capability.Team, Subject: p.Capability.Subject,
		Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, CreatedBy: p.Capability.Subject.ID, IssuedAt: b.snapshot.Now,
	}
	if operator {
		row.Subject = capability.Subject{Kind: capability.SubjectService, ID: "launcher"}
		row.Grants = []capability.Grant{capability.ClaimsLaunch}
	}
	revokes := b.snapshot.Now.Add(time.Hour)
	f := &capabilityManagement{team: store.Team(row.Team), rows: []capability.Metadata{row}}
	f.result = capability.Rotation{Previous: row, RevokesAt: revokes, Issued: capability.Issued{Raw: "new-private-bearer", Capability: capability.Capability{
		Prefix: "replacement", Team: row.Team, Subject: row.Subject, Audience: row.Audience, Grants: row.Grants,
		Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: b.snapshot.Now,
	}}}
	return b, f
}

func managementServer(b *credentialBoundary, f *capabilityManagement) http.Handler {
	return bindTenantPort[controller.CapabilityManager](controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithOperatorCapabilityManager(f),
		func(_ context.Context, team store.Team) (controller.CapabilityManager, error) {
			if team != f.team {
				return nil, errors.New("wrong resolver scope")
			}
			return f, nil
		}).Handler()
}

func managementPath(operator bool) string {
	if operator {
		return "/api/v1/operator/tokens"
	}
	return "/api/v1/tokens"
}

func TestCapabilityManagementHTTPReads(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, lookup := range []bool{false, true} {
			b, f := managementFixture(operator)
			path := managementPath(operator)
			if lookup {
				path += "/target"
			}
			w := authorityHTTP(managementServer(b, f), b, "GET", path+"?team=foreign", "")
			if w.Code != 200 || f.calls != 1 || f.auth.Target.Team != string(f.team) || f.auth.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s auth=%+v calls=%d", w.Code, w.Body.String(), f.auth, f.calls)
			}
			var got capability.Metadata
			if lookup {
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if f.auth.Target.TokenPrefix != "target" || !operator && b.bindingRule != capability.BindOwnToken {
					t.Fatal("lookup lost target binding")
				}
			} else {
				var list struct {
					Tokens []capability.Metadata `json:"tokens"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Tokens) != 1 {
					t.Fatalf("list=%s err=%v", w.Body.String(), err)
				}
				got = list.Tokens[0]
				if b.bindingRule != capability.BindNone {
					t.Fatal("list requires an existing token")
				}
			}
			if got.Prefix != "target" || got.Subject != f.rows[0].Subject || got.IssuedAt != b.snapshot.Now {
				t.Fatalf("metadata=%+v", got)
			}
		}
	}
}

func TestCapabilityManagementHTTPPreservesCommittedGrace(t *testing.T) {
	b, f := managementFixture(false)
	deadline := b.snapshot.Now.Add(time.Hour)
	f.rows[0].RevokedAt = &deadline
	w := authorityHTTP(managementServer(b, f), b, "GET", "/api/v1/tokens", "")
	var result struct {
		Tokens []capability.Metadata `json:"tokens"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Tokens) != 1 || result.Tokens[0].RevokedAt == nil || !result.Tokens[0].RevokedAt.Equal(deadline) || f.filter.IncludeRevoked {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestCapabilityManagementHTTPFiltersAndMethods(t *testing.T) {
	for _, operator := range []bool{false, true} {
		b, f := managementFixture(operator)
		h := managementServer(b, f)
		path := managementPath(operator)
		for _, query := range []string{"?kind=bogus", "?kind=account&kind=machine", "?include_revoked=yes", "?include_revoked=1&include_revoked=0", "?kind=%zz"} {
			w := authorityHTTP(h, b, "GET", path+query, "")
			assertEngineError(t, w, 400, apierr.BadRequest)
		}
		if f.calls != 0 {
			t.Fatal("invalid filters reached persistence")
		}
		f.rows = nil
		w := authorityHTTP(h, b, "GET", path+"?kind=machine&include_revoked=1", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"tokens":[]`) || f.filter.SubjectKind != capability.SubjectMachine || !f.filter.IncludeRevoked {
			t.Fatalf("HTTP %d %s filter=%+v", w.Code, w.Body.String(), f.filter)
		}
		for _, method := range []string{"HEAD", "PUT", "PATCH"} {
			w := authorityHTTP(h, b, method, path+"/target", "")
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		}
	}
}

func TestCapabilityManagementHTTPRefusals(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, action := range []string{"", "/target", "/target/rotate"} {
			for _, fault := range []string{"absent", "unknown", "expired", "revoked", "ancestry", "membership", "grant", "machine", "binding", "transaction", "private", "missing"} {
				b, f := managementFixture(operator)
				p := &b.snapshot.Authorities[0].Principal
				code, calls := apierr.Forbidden, 0
				switch fault {
				case "absent":
					b.raw, code = "", apierr.Unauthenticated
				case "unknown":
					b.raw, code = "swk_"+strings.Repeat("A", 43), apierr.Unauthenticated
				case "expired":
					p.Capability.ExpiresAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "revoked":
					p.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "ancestry":
					parent := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "ended-root"}
					p.Capability.Parent = &parent
					root := *p
					root.Authority, root.Capability.Prefix, root.Capability.Parent, root.Capability.RevokedAt = parent, parent.ID, nil, &b.snapshot.Now
					b.snapshot.Authorities = append(b.snapshot.Authorities, capability.AuthorityState{Principal: root})
					code = apierr.Unauthenticated
				case "membership":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "grant":
					p.Capability.Grants = nil
				case "machine":
					p.Capability.Subject.Kind = capability.SubjectMachine
				case "binding":
					b.bindingErr = &apierr.Error{Code: code, Message: "binding refused"}
				case "transaction":
					f.err, code, calls = &apierr.Error{Code: apierr.Unauthenticated, Message: "authority ended before commit"}, apierr.Unauthenticated, 1
				case "private":
					f.err, code, calls = errors.New("private credential diagnostic"), apierr.Internal, 1
				case "missing":
					f.err, code, calls = store.ErrNotFound, apierr.NotFound, 1
				}
				method := "GET"
				if action == "/target/rotate" {
					method = "POST"
				}
				w := authorityHTTP(managementServer(b, f), b, method, managementPath(operator)+action, `{}`)
				assertEngineError(t, w, apierr.Status(code), code)
				if f.calls != calls || strings.Contains(w.Body.String(), "new-private-bearer") || strings.Contains(w.Body.String(), "private credential") {
					t.Fatalf("fault=%s calls=%d HTTP %d %s", fault, f.calls, w.Code, w.Body.String())
				}
			}
		}
	}
}

func TestCapabilityManagementHTTPScopeProperty(t *testing.T) {
	property := func(value []byte, mutation uint8, operator, rotate bool) bool {
		b, f := managementFixture(operator)
		row := &f.rows[0]
		switch mutation % 4 {
		case 0:
			row.Team = "foreign-" + hex.EncodeToString(value)
		case 1:
			row.Audience = capability.NodeHost
		case 2:
			row.Subject.Kind = capability.SubjectAnonymous
		case 3:
			if operator {
				row.Subject.Kind = capability.SubjectMachine
			} else {
				row.Subject.ID, row.CreatedBy = "foreign-"+hex.EncodeToString(value), "foreign"
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
			}
		}
		method, suffix := "GET", ""
		if rotate {
			f.result.Previous = *row
			method, suffix = "POST", "/target/rotate"
		}
		w := authorityHTTP(managementServer(b, f), b, method, managementPath(operator)+suffix, `{}`)
		var result apierr.Envelope
		return w.Code == 500 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && !strings.Contains(w.Body.String(), "new-private-bearer") && !strings.Contains(w.Body.String(), row.Subject.ID)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityManagementHTTPRotation(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			b, f := managementFixture(operator)
			body, grace := `{}`, 24*time.Hour
			if explicit {
				body, grace = `{"grace_secs":604800,"expires_at":"2100-01-01T00:00:00Z"}`, 7*24*time.Hour
				prior, next := b.snapshot.Now.Add(time.Hour), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
				f.result.Previous.ExpiresAt, f.result.Issued.Capability.ExpiresAt = &prior, &next
			}
			w := authorityHTTP(managementServer(b, f), b, "POST", managementPath(operator)+"/target/rotate", body)
			var result struct {
				Capability   string
				OldPrefix    string    `json:"old_prefix"`
				OldRevokedAt time.Time `json:"old_revoked_at"`
				New          capability.Metadata
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Capability != f.result.Issued.Raw || result.OldPrefix != "target" || result.OldRevokedAt != f.result.RevokesAt || result.New.Prefix != "replacement" || f.grace != grace || (f.expires != nil) != explicit || f.auth.Target.TokenPrefix != "target" {
				t.Fatalf("HTTP %d %s grace=%s expires=%v", w.Code, w.Body.String(), f.grace, f.expires)
			}
			if explicit && (result.New.ExpiresAt == nil || !result.New.ExpiresAt.Equal(*f.expires)) {
				t.Fatalf("replacement lost its committed expiry: %s", w.Body.String())
			}
		}
		for _, body := range []string{`{"grace_secs":-1}`, `{"grace_secs":604801}`, `{"grace_secs":9223372036854775807}`, `{"grace_secs":null}`, `{"expires_at":null}`, `{"expires_at":"0001-01-01T00:00:00Z"}`, `{"team":"foreign"}`, `{"grants":[]}`, `{"ttl_secs":1}`, `{} {}`, `[]`} {
			b, f := managementFixture(operator)
			w := authorityHTTP(managementServer(b, f), b, "POST", managementPath(operator)+"/target/rotate", body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if f.calls != 0 {
				t.Fatal("invalid rotation reached persistence")
			}
		}
	}
}

func TestCapabilityManagementHTTPReplacementProperty(t *testing.T) {
	property := func(value []byte, mutation uint8, operator bool) bool {
		b, f := managementFixture(operator)
		c := &f.result.Issued.Capability
		foreign := "foreign-" + hex.EncodeToString(value)
		switch mutation % 11 {
		case 0:
			c.Team = foreign
		case 1:
			c.Subject.ID = foreign
		case 2:
			c.Grants = append(c.Grants, capability.OperatorConsole)
		case 3:
			c.Audience = capability.NodeHost
		case 4:
			c.Binding = capability.Binding{Kind: capability.BindingRun, RunID: foreign}
		case 5:
			c.Unit = capability.UnitNode
		case 6:
			c.Prefix = f.result.Previous.Prefix
		case 7:
			f.result.Previous.Prefix = foreign
		case 8:
			c.ExpiresAt = &c.IssuedAt
		case 9:
			c.RevokedAt = &b.snapshot.Now
		case 10:
			c.RootMetered = !f.result.Previous.Metered
		}
		w := authorityHTTP(managementServer(b, f), b, "POST", managementPath(operator)+"/target/rotate", `{}`)
		return w.Code == 500 && !strings.Contains(w.Body.String(), f.result.Issued.Raw) && !strings.Contains(w.Body.String(), foreign)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityManagementHTTPMissingAdaptersAndCSRF(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, action := range []string{"", "/target", "/target/rotate"} {
			b, f := managementFixture(operator)
			method := "GET"
			if action == "/target/rotate" {
				method = "POST"
			}
			s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
			path := managementPath(operator) + action
			w := authorityHTTP(s.Handler(), b, method, path, `{}`)
			assertEngineError(t, w, 500, apierr.PolicyMissing)
			bindTenantPort[controller.CapabilityManager](s, func(context.Context, store.Team) (controller.CapabilityManager, error) { return f, nil })
			f.team = "foreign"
			w = authorityHTTP(s.Handler(), b, method, path, `{}`)
			assertEngineError(t, w, 500, apierr.PolicyMissing)
			if f.calls != 0 {
				t.Fatal("foreign manager was invoked")
			}
		}
		b, f := managementFixture(operator)
		if operator {
			b = consoleBoundary()
		} else {
			b = issuanceSession()
		}
		h := managementServer(b, f)
		r := httptest.NewRequest("POST", managementPath(operator)+"/target/rotate", strings.NewReader(`{}`))
		r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertEngineError(t, w, 403, apierr.Forbidden)
		if f.calls != 0 {
			t.Fatal("rotation reached persistence without CSRF")
		}
		w = authorityHTTP(h, b, "POST", managementPath(operator)+"/target/rotate", `{"grace_secs":0}`)
		if w.Code != 200 || f.grace != 0 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/team/tokens", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("legacy inventory: HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestCapabilityManagementHTTPAuditProperty(t *testing.T) {
	property := func(value []byte, operator, complete, role bool) bool {
		b, f := managementFixture(operator)
		digest := sha256.Sum256(value)
		raw := "swk_" + base64.RawURLEncoding.EncodeToString(digest[:])
		f.rows[0].Prefix = raw[:16]
		logs := &bytes.Buffer{}
		s := bindTenantPort[controller.CapabilityManager](controller.New(nil, slog.New(slog.NewTextHandler(logs, nil))).WithAuthorization(consoleAuthorization(b)).WithOperatorCapabilityManager(f),
			func(context.Context, store.Team) (controller.CapabilityManager, error) { return f, nil })
		selector := raw[:16]
		if complete {
			selector = raw
		}
		method, path, body := "GET", managementPath(operator)+"/"+selector, ""
		status := 200
		if complete {
			status = 500
		}
		if role {
			method, path, body, status = "PATCH", "/api/v1/team/members/member", `{"role":"`+raw+`"}`, 400
			if operator {
				status = 403
			}
		}
		w := authorityHTTP(s.Handler(), b, method, path, body)
		loggedTarget := strings.Contains(logs.String(), "token_prefix=")
		wantTarget := !role && !complete
		ok := w.Code == status && loggedTarget == wantTarget && (!wantTarget || strings.Contains(logs.String(), "token_prefix="+f.rows[0].Prefix)) &&
			!strings.Contains(logs.String(), "role=") && !strings.Contains(logs.String(), raw) && !strings.Contains(logs.String(), b.raw) && !strings.Contains(logs.String(), f.result.Issued.Raw)
		if !ok {
			t.Logf("HTTP %d (want %d), target logged=%t (want %t): %s", w.Code, status, loggedTarget, wantTarget, logs.String())
		}
		return ok
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}
