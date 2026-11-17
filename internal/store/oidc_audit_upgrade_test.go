package store_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

const oidcSchema39Digest = "bd376ce39b5fdee54f286dd73111f3644264b8cc4cb864a887cd589436eae19b"

func prepareOIDCSchema39(t *testing.T, st *store.Store) {
	t.Helper()
	for _, q := range []string{`DROP TABLE IF EXISTS audit_privacy_migrations`, `UPDATE sparkwing_schema_version SET version=39,baseline_digest='` + oidcSchema39Digest + `'`} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOIDCAuditUpgradeScrubsHistoricalRowsAndMatchesCandidates(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	personal := "person@example.test"
	subject := "team:default:pipeline:" + personal + ":ref:refs/heads/" + personal
	raw := `{"node_digest":"` + strings.Repeat("a", 64) + `","audience":"` + personal + `","token_id":"token-random","subject":"` + subject + `","expires_at":42}`
	if _, err := st.DB().Exec(storetest.Rebind(st, `INSERT INTO audit_events(id,ts,kind,attrs,run_id,actor) VALUES ('old-oidc',1,'oidc.minted',?,'run-random','controller'),('other',2,'run.deleted','{}','run-other','controller')`), raw); err != nil {
		t.Fatal(err)
	}
	var recorded int64
	if err := st.DB().QueryRow(`SELECT recorded_at FROM audit_events WHERE id='old-oidc'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	prepareOIDCSchema39(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st = target.Open(t, store.WithPolicies(func(*store.Store) (store.Policies, error) { return store.Policies{AuditDigests: cipher}, nil }))
		var body string
		if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE id='old-oidc'`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, personal) || strings.Contains(body, `"audience":`) || strings.Contains(body, `"subject":`) {
			t.Fatalf("raw OIDC identity survived upgrade: %s", body)
		}
		derive := hmac.New(sha256.New, bytes.Repeat([]byte{9}, 32))
		derive.Write([]byte("sparkwing oidc audit key v1"))
		key := derive.Sum(nil)
		attrs := map[string]any{}
		if err := json.Unmarshal([]byte(body), &attrs); err != nil {
			t.Fatal(err)
		}
		for field, value := range map[string]string{"audience": personal, "subject": subject} {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte("sparkwing oidc audit v1\x00" + field + "\x00" + value))
			if attrs[field+"_digest"] != hex.EncodeToString(mac.Sum(nil)) {
				t.Fatalf("candidate does not match %s", field)
			}
		}
		doc, err := docs.ReadRaw("oidc")
		if err != nil {
			t.Fatal(err)
		}
		for field := range attrs {
			if !strings.Contains(doc, "`"+field+"`") {
				t.Fatalf("stored OIDC field %s lacks documentation", field)
			}
		}
		if attrs["token_id"] != "token-random" || attrs["expires_at"] != float64(42) || len(attrs) != 6 {
			t.Fatalf("unrelated attrs changed: %s", body)
		}
		var metadata string
		if err := st.DB().QueryRow(`SELECT id||':'||ts||':'||recorded_at||':'||kind||':'||run_id||':'||actor FROM audit_events WHERE id='old-oidc'`).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		if metadata != fmt.Sprintf("old-oidc:1:%d:oidc.minted:run-random:controller", recorded) {
			t.Fatalf("metadata changed: %s", metadata)
		}
		var audits int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("scrub receipts=%d: %v", audits, err)
		}
		for _, q := range []string{`UPDATE audit_events SET attrs='{}'`, `UPDATE audit_events SET actor='forged'`, `DELETE FROM audit_events`} {
			if _, err := st.DB().Exec(q); err == nil {
				t.Fatalf("mutation accepted: %s", q)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func oidcAuditPolicies(t *testing.T) store.Option {
	t.Helper()
	cipher := drillCipher(t)
	return store.WithPolicies(func(*store.Store) (store.Policies, error) { return store.Policies{AuditDigests: cipher}, nil })
}

func seedHistoricalOIDCAudit(t *testing.T, st *store.Store, body string) {
	t.Helper()
	q := storetest.Rebind(st, `INSERT INTO audit_events(id,ts,kind,attrs) VALUES ('old',1,'oidc.minted',?),('other',1,'run.deleted','{}')`)
	if _, err := st.DB().Exec(q, body); err != nil {
		t.Fatal(err)
	}
}

const historicalOIDCAttrs = `{"node_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","audience":"person@example.test","token_id":"opaque-token","subject":"team:default:pipeline:person@example.test","expires_at":42}`

func TestOIDCAuditScrubExceptionAllowsOnlyExactPayload(t *testing.T) {
	st := storetest.New(t).Open(t, oidcAuditPolicies(t))
	seedHistoricalOIDCAudit(t, st, historicalOIDCAttrs)
	store.ExerciseOIDCScrubGuardForTest(t, st, func(exec func(string, ...any) error) {
		if st.Dialect() == store.DialectPostgres {
			if err := exec(`TRUNCATE audit_events`); err == nil {
				t.Fatal("scrub allowed truncation")
			}
		}
		exact := `UPDATE audit_events SET attrs=(SELECT new_attrs FROM oidc_audit_scrub_rows WHERE id='old') WHERE id='old'`
		if err := exec(exact); err != nil {
			t.Fatalf("exact scrub refused: %v", err)
		}
		for _, q := range []string{
			`UPDATE audit_events SET attrs='{}' WHERE id='old'`,
			`UPDATE audit_events SET attrs=(SELECT new_attrs FROM oidc_audit_scrub_rows WHERE id='old') WHERE id='other'`,
			`DELETE FROM audit_events`,
			`UPDATE sparkwing_schema_version SET version=39; ` + exact,
			`UPDATE audit_privacy_migrations SET completed=1; ` + exact,
		} {
			if err := exec(q); err == nil {
				t.Fatalf("scrub bypass accepted: %s", q)
			}
		}
		for column, value := range map[string]string{"id": "'changed'", "recorded_at": "1", "ts": "2", "team": "'other'", "run_id": "'changed'", "account": "'changed'", "kind": "'run.deleted'", "subject_id": "'changed'", "actor": "'changed'", "actor_is_account": "1", "subject_is_account": "1"} {
			q := `UPDATE audit_events SET attrs=(SELECT new_attrs FROM oidc_audit_scrub_rows WHERE id='old'),` + column + `=` + value + ` WHERE id='old'`
			if err := exec(q); err == nil {
				t.Fatalf("scrub changed %s", column)
			}
		}
	})
	if _, err := st.DB().Exec(`UPDATE audit_events SET attrs='{}' WHERE id='old'`); err == nil {
		t.Fatal("rollback left the exception open")
	}
}

func TestOIDCAuditScrubRollbackRetryAndMalformedHistory(t *testing.T) {
	for _, mode := range []string{"audit-failure", "key-unavailable", "malformed", "partial", "empty"} {
		t.Run(mode, func(t *testing.T) {
			target := storetest.New(t)
			st := target.Open(t)
			body := historicalOIDCAttrs
			if mode == "malformed" {
				body = `{broken`
			}
			if mode == "partial" {
				body = `{"audience":"person@example.test"}`
			}
			if mode != "empty" {
				seedHistoricalOIDCAudit(t, st, body)
			}
			prepareOIDCSchema39(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st = target.Open(t)
			if mode == "audit-failure" {
				trigger := `CREATE TRIGGER refuse_privacy_receipt BEFORE INSERT ON audit_events WHEN NEW.kind='audit.oidc_scrubbed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`
				if st.Dialect() == store.DialectPostgres {
					trigger = `CREATE FUNCTION refuse_privacy_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='audit.oidc_scrubbed' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_privacy_receipt BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION refuse_privacy_receipt()`
				}
				if _, err := st.DB().Exec(trigger); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			policies := oidcAuditPolicies(t)
			if mode == "key-unavailable" {
				policies = store.WithPolicies(func(*store.Store) (store.Policies, error) {
					return store.Policies{AuditDigests: (*secrets.Cipher)(nil)}, nil
				})
			}
			opened, err := target.TryOpen(policies)
			if mode == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				if err := opened.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				_ = opened.Close()
				t.Fatal("failed scrub published a store")
			}
			st = target.Open(t)
			var unchanged string
			if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE id='old'`).Scan(&unchanged); err != nil || unchanged != body {
				t.Fatalf("rollback changed history: %v", err)
			}
			var completed, receipts int
			if err := st.DB().QueryRow(`SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&completed); err != nil || completed != 0 {
				t.Fatalf("rollback completed marker: %v", err)
			}
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("rollback receipt: %v", err)
			}
			if _, err := st.DB().Exec(`UPDATE audit_events SET attrs='{}' WHERE id='old'`); err == nil {
				t.Fatal("rollback removed guard")
			}
			if mode == "audit-failure" {
				drop := `DROP TRIGGER refuse_privacy_receipt`
				if st.Dialect() == store.DialectPostgres {
					drop += ` ON audit_events`
				}
				if _, err := st.DB().Exec(drop); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st = target.Open(t, oidcAuditPolicies(t))
				if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE id='old'`).Scan(&unchanged); err != nil || strings.Contains(unchanged, "person@example.test") {
					t.Fatalf("retry failed to scrub: %v", err)
				}
			}
		})
	}
}

func TestOIDCAuditConcurrentReconciliationAndRotationPreserveDigests(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	seedHistoricalOIDCAudit(t, st, historicalOIDCAttrs)
	prepareOIDCSchema39(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	oldKey, newKey := bytes.Repeat([]byte{19}, 32), bytes.Repeat([]byte{20}, 32)
	cipher, err := secrets.NewCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	policies := store.WithPolicies(func(*store.Store) (store.Policies, error) { return store.Policies{AuditDigests: cipher}, nil })
	var workers sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			opened, err := target.TryOpen(policies)
			if err == nil {
				err = opened.Close()
			}
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	st = target.Open(t)
	var before string
	if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE id='old'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("concurrent receipts=%d: %v", receipts, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	rotated, err := secrets.NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	st = target.Open(t, store.WithPolicies(func(*store.Store) (store.Policies, error) { return store.Policies{AuditDigests: rotated}, nil }))
	var after string
	if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE id='old'`).Scan(&after); err != nil || before != after {
		t.Fatalf("rotation rewrote audit: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(after), &raw); err != nil {
		t.Fatal(err)
	}
	var keyID, digest string
	if err := json.Unmarshal(raw["digest_key_id"], &keyID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw["audience_digest"], &digest); err != nil {
		t.Fatal(err)
	}
	_, matched, err := rotated.OIDCAuditDigest(keyID, "audience", "person@example.test")
	if err != nil || matched != digest {
		t.Fatalf("retired key failed matching: %v", err)
	}
}

func TestOIDCAuditScrubCoversEveryPageAndEmptyID(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	tx, err := st.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := range 260 {
		id := fmt.Sprintf("page-%03d", i)
		if i == 0 {
			id = ""
		}
		if _, err := tx.ExecContext(t.Context(), storetest.Rebind(st, `INSERT INTO audit_events(id,ts,kind,attrs) VALUES (?,1,'oidc.minted',?)`), id, historicalOIDCAttrs); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	prepareOIDCSchema39(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = target.Open(t, oidcAuditPolicies(t))
	var remaining int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='oidc.minted' AND attrs LIKE '%person@example.test%'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("raw rows left across pages: %d, %v", remaining, err)
	}
	var receipt string
	if err := st.DB().QueryRow(`SELECT attrs FROM audit_events WHERE kind='audit.oidc_scrubbed'`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	var attrs struct {
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal([]byte(receipt), &attrs); err != nil || attrs.Rows != 260 {
		t.Fatalf("scrub count=%d: %v", attrs.Rows, err)
	}
}
