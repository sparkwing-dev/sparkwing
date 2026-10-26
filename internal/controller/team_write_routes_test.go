package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type teamWriteFake struct {
	RunTransactions
	team  store.Team
	seen  capability.Authorized
	value string
	calls int
	err   error
}

var _ gitHubAppWriter = (*teamWriteFake)(nil)

func (f *teamWriteFake) ListFailedGitHubDeliveries(_ context.Context, a capability.Authorized) ([]store.FailedGitHubDelivery, error) {
	return nil, f.record(a, "list failed deliveries")
}

func (f *teamWriteFake) ProcessGitHubDeliveryAgain(_ context.Context, a capability.Authorized, id string) error {
	return f.record(a, id)
}

func (f *teamWriteFake) Team() store.Team { return f.team }
func (f *teamWriteFake) ReadRun(_ context.Context, id string) (store.RunRow, error) {
	return store.RunRow{Team: f.team, Execution: runstate.Execution{ID: id}}, nil
}

func (f *teamWriteFake) record(a capability.Authorized, value string) error {
	f.seen, f.value = a, value
	f.calls++
	return f.err
}

func (f *teamWriteFake) RenameAuthorizedTeam(_ context.Context, a capability.Authorized, name string) (store.TeamInfo, error) {
	return store.TeamInfo{Slug: f.team, DisplayName: name}, f.record(a, name)
}

func (f *teamWriteFake) DeleteAuthorizedTeam(_ context.Context, a capability.Authorized) (store.TeamDeletion, error) {
	return store.TeamDeletion{Team: f.team, State: store.TeamDeletionPending}, f.record(a, "delete team")
}

func (f *teamWriteFake) CreateAuthorizedInvitation(_ context.Context, a capability.Authorized, email string, role store.Role, key string) (store.Invitation, error) {
	return store.Invitation{ID: "invite", Team: f.team, Email: email, Role: role}, f.record(a, key)
}

func (f *teamWriteFake) DeleteAuthorizedInvitation(_ context.Context, a capability.Authorized, id string) error {
	return f.record(a, id)
}

func (f *teamWriteFake) SetAuthorizedBuildTrust(_ context.Context, a capability.Authorized, trust bool) error {
	return f.record(a, fmt.Sprint(trust))
}

func (f *teamWriteFake) DeleteAuthorizedRun(_ context.Context, a capability.Authorized) error {
	return f.record(a, a.Target.RunID)
}

func (f *teamWriteFake) UnbindAuthorizedGitHubInstallation(_ context.Context, a capability.Authorized, id int64) error {
	return f.record(a, fmt.Sprint(id))
}

func (f *teamWriteFake) PutAuthorizedGitHubTrigger(_ context.Context, a capability.Authorized, row store.GitHubAppTrigger) (store.GitHubAppTrigger, error) {
	return row, f.record(a, row.Repository)
}

func (f *teamWriteFake) DeleteAuthorizedGitHubTrigger(_ context.Context, a capability.Authorized, id int64, pipeline string) error {
	return f.record(a, fmt.Sprint(id)+pipeline)
}

func (f *teamWriteFake) PutAuthorizedGitHubAutomation(_ context.Context, a capability.Authorized, row store.GitHubAppAutomation) error {
	return f.record(a, row.Repository)
}

func (f *teamWriteFake) DeleteAuthorizedGitHubAutomation(_ context.Context, a capability.Authorized, id int64) error {
	return f.record(a, fmt.Sprint(id))
}

func (f *teamWriteFake) SetAuthorizedGitHubExtraRepos(_ context.Context, a capability.Authorized, repo string, extras []string, _ map[string]int64) ([]string, error) {
	return extras, f.record(a, repo)
}

func TestTeamWriteHTTPAuthorityProperty(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = append(f.facts.snapshot.Authorities[0].Principal.Capability.Grants, capability.TeamAdmin)
	writer := &teamWriteFake{team: store.DefaultTeam}
	f.s.githubApp = &githubAppState{}
	f.s.WithEngineController(&EngineController{ResolveTenant: func(context.Context, store.Team) (TenantHandle, error) { return writer, nil }})
	cases := []struct {
		method, path, body string
		status             int
	}{
		{"PATCH", "/api/v1/team", `{"display_name":"Label"}`, 200},
		{"DELETE", "/api/v1/team", `{"confirm_slug":"default"}`, 202},
		{"POST", "/api/v1/team/invitations", `{"email":"friend@example.com","role":"reader"}`, 201},
		{"DELETE", "/api/v1/team/invitations/invite", "", 204},
		{"PUT", "/api/v1/team/build-trust", `{"trust_local_builds":true}`, 200},
		{"DELETE", "/api/v1/runs/run", "", 204},
		{"DELETE", "/api/v1/team/github-app/installations/17", "", 204},
		{"DELETE", "/api/v1/team/github-app/triggers?repository_id=23&pipeline=ci", "", 204},
		{"DELETE", "/api/v1/team/github-app/automation?repository_id=23", "", 204},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			property := func(selector []byte, phase uint8) bool {
				writer.calls, writer.err, writer.team = 0, nil, store.DefaultTeam
				want := tc.status
				switch phase % 4 {
				case 1:
					writer.team = "foreign"
					want = 500
				case 2:
					writer.err = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}
					want = 409
				case 3:
					writer.err = errors.New("private-database-password")
					want = 500
				}
				path := tc.path
				sep := "?"
				if strings.Contains(path, "?") {
					sep = "&"
				}
				if !strings.Contains(path, "/automation") {
					path += sep + "team=foreign&actor=" + fmt.Sprintf("%x", selector)
				}
				w := f.call(t, tc.method, path, tc.body, "request-key", want, nil)
				if strings.Contains(w.Body.String(), "private-database-password") {
					return false
				}
				if phase%4 == 1 {
					return writer.calls == 0
				}
				return writer.calls == 1 && writer.seen.Target.Team == string(store.DefaultTeam) && writer.seen.Principal.Capability.Subject.ID == "pusher" && (tc.method != "POST" || writer.value == "request-key")
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type billingWriterFake struct {
	team                        store.Team
	seen                        capability.Authorized
	page                        capability.BillingPage
	prepared, completed, failed int
	err                         error
	completeErr                 error
	refill                      store.RefillSettings
}

func (f *billingWriterFake) Team() store.Team { return f.team }
func (f *billingWriterFake) TeamOwnerEmails(context.Context) ([]string, error) {
	return []string{"owner@example.test"}, nil
}
func (f *billingWriterFake) PrepareAuthorizedBillingPage(_ context.Context, a capability.Authorized, kind capability.BillingPageKind, amount int64, key string) (capability.BillingPage, error) {
	f.prepared++
	f.seen = a
	if key != "request-key" {
		return capability.BillingPage{}, errors.New("wrong replay key")
	}
	page := f.page
	page.Kind, page.Team, page.AmountCents = kind, string(f.team), amount
	return page, f.err
}

func (f *billingWriterFake) CompleteAuthorizedBillingPage(_ context.Context, a capability.Authorized, page capability.BillingPage) error {
	f.completed++
	f.seen, f.page = a, page
	return f.completeErr
}

func (f *billingWriterFake) RecordAuthorizedBillingPageFailure(_ context.Context, _ capability.Authorized, _ capability.BillingPage) error {
	f.failed++
	return nil
}

func (f *billingWriterFake) SetAuthorizedRefill(_ context.Context, a capability.Authorized, settings store.RefillSettings) error {
	f.seen, f.refill = a, settings
	return f.err
}

func (f *billingWriterFake) RequestAuthorizedStanding(_ context.Context, a capability.Authorized, tier, message string) (store.StandingRequest, error) {
	f.seen = a
	return store.StandingRequest{ID: "standing-fake", Team: f.team, Tier: tier, Message: message, State: store.StandingRequestOpen}, f.err
}

type billingPageTransport func(*http.Request) (*http.Response, error)

func (f billingPageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBillingPageHTTPAuthorityAndReplayProperty(t *testing.T) {
	f := newDeploymentScheduleHTTPFixture(t, deploymenttest.Metered(t))
	f.s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = append(f.facts.snapshot.Authorities[0].Principal.Capability.Grants, capability.TeamAdmin)
	writer := &billingWriterFake{team: store.DefaultTeam}
	testOnlyTenantPort[billingWriter](f.s, func(context.Context, store.Team) (billingWriter, error) { return writer, nil })
	calls := 0
	mode := uint8(0)
	f.s.checkout = &billingCheckout{url: "https://checkout.example", token: "private-service-key", http: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["team"] != "default" || body["key"] != "reservation" || r.Header.Get("Authorization") != "Bearer private-service-key" {
			t.Fatalf("provider request %v %v", body, r.Header)
		}
		if writer.prepared != 1 || writer.completed != 0 {
			t.Fatal("provider ran outside reservation order")
		}
		if mode == 3 {
			return nil, errors.New("private-provider-password")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"url":"https://checkout.example/page","id":"cs_17","setup_intent":"seti_17","payment_intent":"pi_17","expires_at":200}`))}, nil
	})}}
	for _, tc := range []struct {
		path, body string
		kind       capability.BillingPageKind
	}{
		{"checkout", `{"amount_cents":1000}`, capability.BillingPageCredits},
		{"card", "", capability.BillingPageCard},
	} {
		t.Run(tc.path, func(t *testing.T) {
			property := func(selector []byte, phase uint8) bool {
				*writer = billingWriterFake{team: store.DefaultTeam, page: capability.BillingPage{ReservationID: "reservation", ExpiresAt: time.Unix(190, 0), Customer: "cus_17"}}
				calls, mode = 0, phase%5
				want := 200
				switch mode {
				case 1:
					writer.err = &apierr.Error{Code: apierr.Conflict, Message: "refused"}
					want = 409
				case 2:
					writer.page.URL = "https://checkout.example/replay"
				case 3:
					want = 503
				case 4:
					writer.completeErr = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}
					want = 409
				}
				w := f.call(t, "POST", "/api/v1/team/billing/"+tc.path+"?team=foreign&actor="+fmt.Sprintf("%x", selector), tc.body, "request-key", want, nil)
				if strings.Contains(w.Body.String(), "private-provider-password") {
					return false
				}
				if writer.prepared != 1 || writer.seen.Target.Team != "default" || writer.seen.Principal.Capability.Subject.ID != "pusher" {
					return false
				}
				switch mode {
				case 1, 2:
					return calls == 0 && writer.completed == 0
				case 3:
					return calls == 1 && writer.completed == 0 && writer.failed == 1
				default:
					return calls == 1 && writer.completed == 1 && writer.page.Kind == tc.kind && writer.page.ExpiresAt.Equal(time.Unix(190, 0))
				}
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
				t.Fatal(err)
			}
		})
	}
	writer.err = nil
	f.call(t, "PUT", "/api/v1/team/billing/refill", `{"enabled":true,"threshold_cents":500,"amount_cents":2500}`, "", 204, nil)
	if writer.refill != (store.RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 2500}) {
		t.Fatalf("refill settings lost: %+v", writer.refill)
	}
	for _, tc := range []struct{ path, body string }{
		{"checkout", `{"amount_cents":1000,"team":"foreign"}`}, {"card", `{}`},
	} {
		f.call(t, "POST", "/api/v1/team/billing/"+tc.path, tc.body, "request-key", 400, nil)
	}
}
