package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOperatorCreateTeamHTTP(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/teams", `{"slug":"support-team","display_name":"Support team"}`, 201)
	var created struct {
		Team       string             `json:"team"`
		AccountID  string             `json:"account_id"`
		Capability string             `json:"capability"`
		Prefix     string             `json:"prefix"`
		Grants     []capability.Grant `json:"grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Team != "support-team" || created.AccountID == "" || created.Capability == "" || created.Prefix == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("incomplete team creation: %s", w.Body)
	}
	if !slices.Equal(created.Grants, []capability.Grant{capability.RunsRead, capability.LogsRead, capability.RunsWrite, capability.RunsControl, capability.ApprovalsWrite, capability.TeamAdmin}) {
		t.Fatalf("first admin grants: %v", created.Grants)
	}
	p, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(created.Capability)))
	if err != nil || p.Capability.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: created.AccountID}) || p.Capability.Parent != nil {
		t.Fatalf("admin root: %+v %v", p, err)
	}
	engineClaimHTTP(t, h, created.Capability, "GET", "/api/v1/team/members", "", 200)
	engineClaimHTTP(t, h, created.Capability, "PATCH", "/api/v1/team", `{"display_name":"Renamed team"}`, 200)
	engineClaimHTTP(t, h, created.Capability, "POST", "/api/v1/operator/teams", `{"slug":"another-team"}`, 403)
	tenant, err := e.Store.ForTeam(t.Context(), store.Team(created.Team))
	if err != nil {
		t.Fatal(err)
	}
	events, err := tenant.BusinessEvents(t.Context(), store.BusinessEventTeamCreated, 1000, "")
	if err != nil || len(events) != 1 || !strings.HasPrefix(events[0].Actor, "operator:") || events[0].Account != created.AccountID {
		t.Fatalf("operator audit: %+v %v", events, err)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/teams", `{"slug":"support-team"}`, 409)
	engineClaimHTTP(t, h, created.Capability, "POST", "/api/v1/tokens/"+created.Prefix+"/rotate", `{"grace_secs":0}`, 200)
}

func TestOperatorCreateTeamCloudHTTP(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	seed := reviewSeed(t, e.Store)
	s := NewDeployment(e.Store, deploymenttest.Cloud(t, "https://controller.test"), nil)
	h := s.Handler()
	w := engineClaimHTTP(t, h, seed, "POST", "/api/v1/operator/tokens", `{"name":"Jane Doe; jane@example.test; phone 208-555-0100","grants":["operator.tokens"]}`, 201)
	var operator credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &operator); err != nil {
		t.Fatal(err)
	}
	w = engineClaimHTTP(t, h, operator.Capability, "POST", "/api/v1/operator/teams", `{"slug":"cloud-team"}`, 201)
	var admin credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "cloud-team")
	if err != nil {
		t.Fatal(err)
	}
	events, err := tenant.BusinessEvents(t.Context(), store.BusinessEventTeamCreated, 1000, "")
	if err != nil || len(events) != 1 || !strings.HasPrefix(events[0].Actor, "service:") {
		t.Fatalf("Cloud operator audit: %+v %v", events, err)
	}
	account, err := e.Store.AsOperator().Account(t.Context(), events[0].Account)
	if err != nil || account.Email != "" || account.EmailVerified {
		t.Fatalf("operator-created account: %+v %v", account, err)
	}
	engineClaimHTTP(t, h, operator.Capability, "DELETE", "/api/v1/operator/tokens/"+operator.Prefix, "", 204)
	var actor, attrs string
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT actor,attrs FROM audit_events WHERE kind='team.mutated' AND subject_id=?`, string(capability.AuthorityCapability)+":"+operator.Prefix).Scan(&actor, &attrs); err != nil || actor != events[0].Actor || strings.Contains(attrs, "Jane") {
		t.Fatalf("token revocation retained its display name: actor=%q attrs=%s err=%v", actor, attrs, err)
	}
	engineClaimHTTP(t, h, operator.Capability, "POST", "/api/v1/operator/teams", `{"slug":"refused-team"}`, 401)
	engineClaimHTTP(t, h, admin.Capability, "GET", "/api/v1/team/members", "", 200)
}

func TestOperatorCreateTeamHTTPRollsBackEveryWrite(t *testing.T) {
	for _, table := range []string{"capabilities", "audit_events", "commit"} {
		t.Run(table, func(t *testing.T) {
			e, _ := engineRuntimeStore(t)
			raw := reviewSeed(t, e.Store)
			if table == "commit" {
				engineSQL(t, e.Store, `CREATE TABLE deferred_failure(team TEXT REFERENCES teams(name) DEFERRABLE INITIALLY DEFERRED)`)
				engineSQL(t, e.Store, `CREATE TRIGGER refuse_team_write AFTER INSERT ON capabilities BEGIN INSERT INTO deferred_failure(team) VALUES ('absent-team'); END`)
			} else {
				engineSQL(t, e.Store, `CREATE TRIGGER refuse_team_write BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT,'private database failure'); END`)
			}
			w := engineClaimHTTP(t, New(e.Store, nil).Handler(), raw, "POST", "/api/v1/operator/teams", `{"slug":"atomic-team"}`, 500)
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "swk_") {
				t.Fatalf("private failure: %s", w.Body)
			}
			var count int
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT (SELECT COUNT(*) FROM accounts)+(SELECT COUNT(*) FROM teams WHERE name='atomic-team')+(SELECT COUNT(*) FROM capabilities WHERE team='atomic-team')+(SELECT COUNT(*) FROM memberships WHERE team='atomic-team')+(SELECT COUNT(*) FROM audit_events WHERE team='atomic-team')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial writes=%d %v", count, err)
			}
		})
	}
}

func TestOperatorCreateTeamHTTPRejectsUntrustedInputs(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	for _, body := range []string{`null`, `{}`, `{"slug":"a"}`, `{"slug":"UPPER"}`, `{"slug":"../foreign"}`, `{"slug":"valid-team","grants":["operator.tokens"]}`, `{"slug":"valid-team","account_id":"foreign"}`, `{"slug":"valid-team","actor":"foreign"}`, `{"slug":"valid-team"} {}`} {
		engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/teams", body, 400)
	}
	engineClaimHTTP(t, h, "", "POST", "/api/v1/operator/teams", "malformed", 401)
	engineClaimHTTP(t, h, raw, "PUT", "/api/v1/operator/teams", "", 405)
	engineSQL(t, e.Store, `INSERT INTO team_deletions(slug,state,requested_at,requested_by) VALUES ('retired-team','pending',1,'operator')`)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/teams", `{"slug":"retired-team"}`, 409)
}

func TestOperatorCreateTeamHTTPAuthorityProperty(t *testing.T) {
	clock := time.Now().UTC()
	e, _ := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return clock }))
	raw := reviewSeed(t, e.Store)
	parent, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, nil).Handler()
	iteration := 0
	property := func(granted, expired, forged bool, adversarial string) bool {
		iteration++
		grants := []capability.Grant{capability.OperatorLimits}
		if granted {
			grants = append(grants, capability.OperatorTokens)
		}
		binding := capability.Binding{Kind: capability.BindingNone}
		expires := clock.Add(time.Minute)
		child, err := e.Store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: grants, Binding: binding, ExpiresAt: &expires})
		if err != nil {
			t.Fatal(err)
		}
		if expired {
			clock = expires
		}
		presentation := child.Raw
		if forged {
			presentation = "swk_" + strings.Repeat("Z", 43)
		}
		slug := fmt.Sprintf("property-team-%d", iteration)
		body, err := json.Marshal(map[string]string{"slug": slug})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/operator/teams?team="+url.QueryEscape(adversarial)+"&operator=true", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+presentation)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Operator", adversarial)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		allowed := granted && !expired && !forged
		_, err = e.Store.TeamInfo(t.Context(), store.Team(slug))
		if allowed {
			if w.Code != 201 || err != nil {
				return false
			}
			tenant, err := e.Store.ForTeam(t.Context(), store.Team(slug))
			if err != nil {
				t.Fatal(err)
			}
			events, err := tenant.BusinessEvents(t.Context(), store.BusinessEventTeamCreated, 1000, "")
			return err == nil && len(events) == 1 && strings.HasPrefix(events[0].Actor, "operator:")
		}
		var count int
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT (SELECT COUNT(*) FROM memberships WHERE team=?)+(SELECT COUNT(*) FROM capabilities WHERE team=?)+(SELECT COUNT(*) FROM audit_events WHERE team=?)`, slug, slug, slug).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return (w.Code == 401 || w.Code == 403) && err != nil && count == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorCreateTeamHTTPConsole(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	s.WithOperatorAccounts([]string{"member"})
	route, ok := capability.LookupRoute("POST", "/api/v1/operator/teams")
	if !ok {
		t.Fatal("operator team route missing")
	}
	authenticated, err := s.storeRouteAuthorization().Authenticator.Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	if err != nil || authenticated.Principal.Class != capability.CredentialConsole || authenticated.Principal.Capability.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: "member"}) {
		t.Fatalf("console authorization changed verified identity: %+v %v", authenticated.Principal, err)
	}
	reviewSessionRequest(t, s, raw, "", "POST", "/api/v1/operator/teams", `{"slug":"csrf-team"}`, 403)
	w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/teams", `{"slug":"console-team"}`, 201)
	var created struct{ Team string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.store.ForTeam(t.Context(), store.Team(created.Team))
	if err != nil {
		t.Fatal(err)
	}
	reviewSessionRequest(t, s, raw, csrf, "PUT", "/api/v1/operator/object-store/quotas/console-team", `{"max_bytes_per_run":22}`, 200)
	w = reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/tokens", `{"name":"service","grants":["operator.tokens"]}`, 201)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	reviewSessionRequest(t, s, raw, csrf, "DELETE", "/api/v1/operator/tokens/"+issued.Prefix, "", 204)
	var actor string
	var isAccount int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT actor,actor_is_account FROM audit_events WHERE kind='team.mutated' AND subject_id=?`, string(capability.AuthorityCapability)+":"+issued.Prefix).Scan(&actor, &isAccount); err != nil || actor != "member" || isAccount != 1 {
		t.Fatalf("console revocation actor: %q account=%d %v", actor, isAccount, err)
	}
	checkActors := func(want string) {
		t.Helper()
		events, err := tenant.BusinessEvents(t.Context(), "", 10, "")
		if err != nil || len(events) != 3 {
			t.Fatalf("console audit: %+v %v", events, err)
		}
		for _, ev := range events {
			if ev.Actor != want {
				t.Fatalf("console action %s/%v actor=%q, want %q", ev.Kind, ev.Attrs["operation"], ev.Actor, want)
			}
		}
	}
	checkActors("member")
	s.WithOperatorAccounts(nil)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/teams", `{"slug":"refused-team"}`, 403)
	teststore.SeedTeam(t, s.store, "ops")
	console, operator := teststore.Console(t, s.store, "ops", "operator")
	operator.Target.AccountID = "member"
	if _, err := console.ConsoleDeleteAccount(t.Context(), operator); err != nil {
		t.Fatal(err)
	}
	checkActors("deleted account")
}

type teamCreationRace struct {
	capability.BindingChecker
	after func()
}

func (b teamCreationRace) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	if err := b.BindingChecker.CheckBinding(ctx, p, rule, target); err != nil {
		return err
	}
	b.after()
	return nil
}

func TestOperatorCreateTeamHTTPRechecksBeforeCommit(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	s := New(e.Store, nil)
	authorization := s.authorization
	authorization.Bindings = teamCreationRace{BindingChecker: authorization.Bindings, after: func() {
		p, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Store.AsOperator().RevokeAuthority(t.Context(), runstate.Actor{Authority: p.Authority}, p.Authority); err != nil {
			t.Fatal(err)
		}
	}}
	s.WithAuthorization(authorization)
	engineClaimHTTP(t, s.Handler(), raw, "POST", "/api/v1/operator/teams", `{"slug":"revoked-team"}`, 401)
	if _, err := e.Store.TeamInfo(t.Context(), "revoked-team"); err == nil {
		t.Fatal("revoked operator created a team")
	}
}

func TestOperatorCreateTeamHTTPConcurrentReplicas(t *testing.T) {
	path := t.TempDir() + "/state.db"
	var handlers []http.Handler
	var raw string
	for range 2 {
		st, err := teststore.OpenExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Error(err)
			}
		})
		if raw == "" {
			raw = reviewSeed(t, st)
		}
		handlers = append(handlers, New(st, nil).Handler())
	}
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var done sync.WaitGroup
	for _, h := range handlers {
		done.Go(func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/operator/teams", strings.NewReader(`{"slug":"concurrent-team"}`))
			r.Header.Set("Authorization", "Bearer "+raw)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			statuses <- w.Code
		})
	}
	close(start)
	done.Wait()
	got := []int{<-statuses, <-statuses}
	slices.Sort(got)
	if !slices.Equal(got, []int{201, 409}) {
		t.Fatalf("creation race: %v", got)
	}
}
