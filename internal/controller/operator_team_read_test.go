package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOperatorTeamViewPagesAuditEvents(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('operator','operator@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','operator','reader',1)`)
	raw, _, _, err := e.Store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "operator"}, tenant.Team(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := e.Server.WithOperatorAccounts([]string{"operator"})
	h := s.Handler()
	for i := range 105 {
		engineSQL(t, e.Store, `INSERT INTO audit_events(id,ts,team,kind,actor,subject_id,run_id,attrs) VALUES (?,?,?,'page',?,?,?,'{"operation":"delete"}')`, fmt.Sprintf("page-%03d", i), i/3, tenant.Team(), fmt.Sprint(i), fmt.Sprintf("subject-%d", i), fmt.Sprintf("run-%d", i))
	}
	var cursor string
	for page, want := range []int{50, 50, 5} {
		r := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/operator/teams/a?cursor="+url.QueryEscape(cursor), nil)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var view operatorTeamJSON
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Events) != want {
			t.Fatalf("page %d: status=%d body=%s", page, w.Code, w.Body)
		}
		if view.Events[0].Actor != fmt.Sprint(104-page*50) || view.Events[0].Kind != "page" {
			t.Fatalf("unexpected first event: %+v", view.Events[0])
		}
		if ev := view.Events[0]; ev.SubjectID != fmt.Sprintf("subject-%d", 104-page*50) || ev.RunID != fmt.Sprintf("run-%d", 104-page*50) || ev.Attrs["operation"] != "delete" {
			t.Fatalf("mutation target or operation lost: %+v", ev)
		}
		cursor = view.NextCursor
		if (cursor == "") != (page == 2) {
			t.Fatalf("page %d cursor=%q", page, cursor)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/operator/teams/a?cursor=missing", nil)
	r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor: %d %s", w.Code, w.Body)
	}
}

func TestOperatorTeamReadRequiresCurrentDeploymentGrantProperty(t *testing.T) {
	clock := time.Now().UTC()
	e, _ := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return clock }))
	raw := reviewSeed(t, e.Store)
	parent, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, nil).Handler()
	property := func(granted, expired, forged bool, adversarial string) bool {
		grants := []capability.Grant{capability.OperatorLimits}
		if granted {
			grants = append(grants, capability.OperatorTokens)
		}
		expires := clock.Add(time.Minute)
		issued, err := e.Store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: &expires})
		if err != nil {
			t.Fatal(err)
		}
		if expired {
			clock = expires
		}
		token := issued.Raw
		if forged {
			token = "swk_" + strings.Repeat("Z", 43)
		}
		r := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/operator/teams/default?team="+url.QueryEscape(adversarial), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Operator", url.QueryEscape(adversarial))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !granted || expired || forged {
			return w.Code == 401 || w.Code == 403
		}
		var fields map[string]any
		return w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &fields) == nil && len(fields) == 2 && fields["team"] == "default" && fields["display_name"] != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 60}); err != nil {
		t.Fatal(err)
	}
}
