package store_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func keylessOIDCAuditPolicies() store.Option {
	return store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{AuditDigests: (*secrets.Cipher)(nil)}, nil
	})
}

func TestOIDCAuditEmptyHistoryCompletesWithoutKey(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	prepareOIDCSchema39(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		opened, err := target.TryOpen(keylessOIDCAuditPolicies())
		if err != nil {
			t.Fatalf("keyless empty-history startup refused: %v", err)
		}
		var completed, receipts int
		if err := opened.DB().QueryRow(`SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&completed); err != nil || completed != 1 {
			t.Fatalf("empty migration incomplete: %v", err)
		}
		if err := opened.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&receipts); err != nil || receipts != 1 {
			t.Fatalf("empty receipts=%d: %v", receipts, err)
		}
		var body string
		if err := opened.DB().QueryRow(`SELECT attrs FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		attrs := map[string]any{}
		if err := json.Unmarshal([]byte(body), &attrs); err != nil {
			t.Fatal(err)
		}
		if attrs["rows"] != float64(0) || attrs["version"] != float64(1) || attrs["digest_key_id"] != nil || len(attrs) != 2 {
			t.Fatalf("keyless completion receipt: %s", body)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func oidcAuditUpdateGuard(t *testing.T, st *store.Store) string {
	t.Helper()
	query := `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='audit_events_no_UPDATE'`
	if st.Dialect() == store.DialectPostgres {
		query = `SELECT replace(pg_get_functiondef('reject_audit_mutation'::regproc),quote_ident(current_schema())||'.reject_audit_mutation','audit_schema.reject_audit_mutation')`
	}
	var definition string
	if err := st.DB().QueryRow(query).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestOIDCAuditScrubRestoresFreshInstallGuard(t *testing.T) {
	fresh := storetest.New(t).Open(t)
	expected := oidcAuditUpdateGuard(t, fresh)
	target := storetest.New(t)
	st := target.Open(t)
	seedHistoricalOIDCAudit(t, st, historicalOIDCAttrs)
	prepareOIDCSchema39(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = target.Open(t, oidcAuditPolicies(t))
	if actual := oidcAuditUpdateGuard(t, st); actual != expected {
		t.Fatalf("post-scrub guard differs from fresh install:\n%s\nwant:\n%s", actual, expected)
	}
}

func TestOIDCAuditCompletedStartupRefusesLateRawRows(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refuse     bool
	}{
		{"audience", `{"audience":"person@example.test"}`, true},
		{"subject", `{"subject":null}`, true},
		{"escaped-key", `{"\u0061udience":"person@example.test"}`, true},
		{"nested-key", `{"nested":{"audience":"person@example.test"}}`, false},
		{"quoted-word", `{"text":"quoted \"subject\" word"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := storetest.New(t)
			st := target.Open(t)
			seedHistoricalOIDCAudit(t, st, historicalOIDCAttrs)
			prepareOIDCSchema39(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st = target.Open(t, oidcAuditPolicies(t))
			if _, err := st.DB().Exec(storetest.Rebind(st, `INSERT INTO audit_events(id,ts,kind,attrs) VALUES ('late',1,'oidc.minted',?)`), tc.body); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := target.TryOpen(keylessOIDCAuditPolicies())
			if tc.refuse {
				if err == nil {
					_ = opened.Close()
					t.Fatal("completed startup accepted a late raw OIDC row")
				}
				if strings.Contains(err.Error(), "person@example.test") {
					t.Fatalf("privacy error disclosed candidate: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("top-level privacy check refused a safe control: %v", err)
				}
				if err := opened.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
