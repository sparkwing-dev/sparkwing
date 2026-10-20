package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRunnerMeteringHTTPCommitsThroughCurrentConsoleSession(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	st := engine.Store
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('operator','operator@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','operator','owner',1)`)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := NewDeployment(st, deploymenttest.Team(t), nil).WithSecretsCipher(cipher).WithOperatorAccounts([]string{"operator"})
	raw, csrf, _, err := st.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "operator"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	root, err := st.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])},
		Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "worker"}, RegistrationID: "runner:a:worker",
		Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(body, csrfValue string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "https://controller.test/api/v1/operator/runners/"+root.Capability.Prefix+"/metered", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, csrfValue)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		body, csrf string
		status     int
	}{
		{`{"metered":true}`, "foreign", 403},
		{`{}`, csrf, 400},
		{`{"metered":true}`, csrf, 200},
	} {
		if w := request(tc.body, tc.csrf); w.Code != tc.status {
			t.Fatalf("metering route = %d %s; want %d", w.Code, w.Body, tc.status)
		}
	}
	current, err := st.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix})
	if err != nil || !current.Capability.RootMetered {
		t.Fatalf("HTTP did not commit root metering: %+v, %v", current, err)
	}
	s.WithOperatorAccounts(nil)
	if w := request(`{"metered":false}`, csrf); w.Code != 403 {
		t.Fatalf("delisted console = %d %s", w.Code, w.Body)
	}
	current, err = st.AsOperator().ResolveAuthority(t.Context(), current.Authority)
	if err != nil || !current.Capability.RootMetered {
		t.Fatalf("refused HTTP changed root metering: %+v, %v", current, err)
	}
}

var _ RunnerMeteringWriter = (*store.Operator)(nil)
