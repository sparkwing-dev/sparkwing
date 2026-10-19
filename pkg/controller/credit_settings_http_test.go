package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func creditPolicyFixture(t *testing.T) (*Server, http.Handler, string, string) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil).WithSecretsCipher(cipher).WithDeployment(deploymenttest.Team(t)).WithOperatorAccounts([]string{"operator"})
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('operator','operator@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,'operator','reader',1)`, store.DefaultTeam)
	raw, csrf, _, err := st.CreateAccountSession(t.Context(), store.Account{ID: "operator"}, store.DefaultTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), raw, csrf
}

func creditPolicyWrite(t *testing.T, h http.Handler, raw, csrf, body string, want int) creditSettingsJSON {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "https://controller.test/api/v1/operator/credits/settings", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.test")
	r.Header.Set(csrfHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	h.ServeHTTP(w, r)
	if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("settings write: %d %s, want %d with no-store", w.Code, w.Body.String(), want)
	}
	var settings creditSettingsJSON
	if want == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
	}
	return settings
}

func TestCreditSettingsHTTPPolicyWritesPersist(t *testing.T) {
	s, h, raw, csrf := creditPolicyFixture(t)
	initial, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil || initial.RateTableSet || initial.GraceSeconds != store.DefaultCreditGraceSeconds || initial.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds || !reflect.DeepEqual(initial.RateTable, store.DefaultCreditRateTable(store.DefaultCreditRateMicro)) {
		t.Fatalf("default policy=%+v: %v", initial, err)
	}
	view := creditPolicyWrite(t, h, raw, csrf, `{"rate_micro_per_second":30000,"grace_seconds":0,"max_charge_seconds":45}`, 200)
	if view.RateMicroPerSecond != 30000 || view.GraceSeconds != 0 || view.MaxChargeSeconds != 45 || view.MicroPerCredit != store.MicroCreditsPerCredit || view.CreditsPerDollar != store.CreditsPerDollar {
		t.Fatalf("scalar policy=%+v", view)
	}
	view = creditPolicyWrite(t, h, raw, csrf, `{"GRACE_SECONDS":1}`, 200)
	if view.GraceSeconds != 1 || view.RateMicroPerSecond != 30000 || view.MaxChargeSeconds != 45 {
		t.Fatalf("unnamed settings changed=%+v", view)
	}
	for _, body := range []string{`{"rate_table":[{"cores":4,"micro_per_second":20000},{"cores":2,"micro_per_second":10000}]}`, `{"rate_table":{"2":10000,"4":20000}}`} {
		view = creditPolicyWrite(t, h, raw, csrf, body, 200)
		if !view.RateTableSet || view.RateMicroPerSecond != 20000 || len(view.RateTable) != 2 || view.RateTable[0].Cores != 2 || view.RateTable[0].MicroPerSecond != 10000 {
			t.Fatalf("installed policy=%+v", view)
		}
	}
	before, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	creditPolicyWrite(t, h, raw, csrf, `{"rate_micro_per_second":99000,"grace_seconds":2}`, 400)
	after, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refused scalar changed policy=%+v: %v", after, err)
	}
}

func TestCreditSettingsHTTPInvalidBodiesLeavePolicyUnchanged(t *testing.T) {
	s, h, raw, csrf := creditPolicyFixture(t)
	before, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{}`, `{"rate_micro_per_second":0}`, `{"rate_micro_per_second":-1}`, `{"rate_micro_per_second":1000000000001}`,
		`{"grace_seconds":-1}`, `{"max_charge_seconds":0}`, `{"max_charge_seconds":86401}`,
		`{"grace_seconds":0,"rate_micro_per_second":-5}`, `{"rate_table":{"4":20000},"grace_seconds":-1}`,
		`{"rate_table":{"4":20000},"grace_seconds":null}`, `{"rate_table":{"4":20000},"grace_seconds":null,"grace_seconds":0}`,
		`{"rate_table":[]}`, `{"rate_table":null}`, `{"rate_table":[{"cores":0,"micro_per_second":1}]}`,
		`{"rate_table":[{"cores":4,"micro_per_second":0}]}`, `{"rate_table":[{"cores":4,"micro_per_second":1},{"cores":4,"micro_per_second":2}]}`,
		`{"rate_table":{"large":1}}`, `{"rate_table":"4=20000"}`, `{"rate_micro_per_second":21000,"rate_table":{"4":20000}}`,
	} {
		t.Run(body, func(t *testing.T) {
			creditPolicyWrite(t, h, raw, csrf, body, 400)
			after, err := s.store.AsOperator().CreditSettings(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused body changed policy=%+v: %v", after, err)
			}
		})
	}
}
