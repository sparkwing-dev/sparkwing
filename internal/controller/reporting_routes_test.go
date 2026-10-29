package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

type reportingFacts struct {
	snapshot capability.AuthenticationSnapshot
	digest   [32]byte
	checks   int
}

func (f *reportingFacts) ReadAuthentication(_ context.Context, key capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if key.Class != capability.CredentialBearer || key.Digest != f.digest {
		return capability.AuthenticationSnapshot{}, sessionUnavailable()
	}
	return f.snapshot, nil
}

func (f *reportingFacts) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	f.checks++
	return nil
}

func TestReportingRoutesRejectInvalidAuthorityProperty(t *testing.T) {
	s := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(&EngineController{
		ResolveTenant: selectorTenant("a", nil),
	})
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	endpoints := append(s.reportingEndpoints(), s.teamReadEndpoints()...)
	endpoints = append(endpoints, s.scheduleEndpoints()...)
	endpoints = append(endpoints, s.runControlEndpoints()...)
	endpoints = append(endpoints, s.teamWriteEndpoints()...)
	endpoints = append(endpoints, s.githubAppEndpoints()...)
	endpoints = append(endpoints, s.billingEndpoints()...)
	for _, endpoint := range endpoints {
		if endpoint.Pattern == "/metrics" {
			continue
		}
		t.Run(endpoint.Method+" "+endpoint.Pattern, func(t *testing.T) {
			property := func(input []byte, mutation uint8) bool {
				run, job := "r"+hex.EncodeToString(input), "j"+hex.EncodeToString(input)
				now := time.Unix(100, 0).UTC()
				p := capability.Principal{
					Class:     capability.CredentialBearer,
					Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "credential"},
					Capability: capability.Capability{
						Prefix: "credential", Digest: sha256.Sum256([]byte(raw)), Team: "a",
						Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"},
						Issuance: capability.Delegate, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.LogsRead, capability.RunsControl, capability.TeamAdmin, capability.ApprovalsWrite},
					},
				}
				status, code := 401, apierr.Unauthenticated
				switch mutation%7 + 1 {
				case 1:
					p.Capability.RevokedAt, status, code = &now, 401, apierr.Unauthenticated
				case 2:
					p.Capability.Grants, status, code = []capability.Grant{capability.NodesClaim}, 403, apierr.Forbidden
				case 3:
					if strings.Contains(endpoint.Pattern, "{run}") {
						p.Capability.Team, status, code = "b", 403, apierr.Forbidden
						if endpoint.ResolveTarget != nil {
							status, code = 404, apierr.NotFound
						}
					} else {
						p.Capability.RevokedAt = &now
					}
				case 4:
					p.Capability.Subject.Kind, status, code = capability.SubjectOperator, 403, apierr.Forbidden
				case 5:
					p.Capability.Audience, status, code = capability.NodeHost, 401, apierr.Unauthenticated
				case 6:
					p.Capability.RevokedAt, status, code = &now, 401, apierr.Unauthenticated
				case 7:
					p.Capability.ExpiresAt, status, code = &now, 401, apierr.Unauthenticated
				}
				facts := &reportingFacts{digest: p.Capability.Digest, snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: p}}}}
				path := strings.NewReplacer("{run}", run, "{job}", job, "{name}", "ci", "{schedule}", "schedule", "{installation}", "17", "{invitation}", "invite").Replace(endpoint.Pattern)
				r := httptest.NewRequest(endpoint.Method, path+"?team=forged&run_id=other&job_id=other&selector="+url.QueryEscape(string(input)), nil)
				r.Header.Set("Authorization", "Bearer "+raw)
				r.Header.Set("X-Sparkwing-Team", "forged")
				w := httptest.NewRecorder()
				s.WithAuthorization(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(facts), Bindings: facts})
				s.Handler().ServeHTTP(w, r)
				if w.Code != status || facts.checks != 0 {
					t.Logf("mutation=%d: HTTP %d %s; binding checks=%d", mutation%7+1, w.Code, w.Body, facts.checks)
					return false
				}
				var failure apierr.Envelope
				return json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error != nil && failure.Error.Code == code
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReportingRetiredRoutes(t *testing.T) {
	s := New(nil, nil)
	h := s.Handler()
	for _, path := range []string{
		"/api/v1/agents", "/api/v1/capacity/profiles", "/api/v1/capacity/profiles/explain", "/api/v1/approvals/pending",
		"/api/v1/runs/run/paused", "/api/v1/runs/run/logs/job", "/api/v1/runs/run/logs/job/stream", "/api/v1/runs/run/logs/job/completeness",
	} {
		for _, bearer := range []string{"", "swct_retired", "swk_unrecognized"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 404 || s.RegisteredRoute(r) {
				t.Fatalf("%s: HTTP %d %s", path, w.Code, w.Body)
			}
		}
	}
}
