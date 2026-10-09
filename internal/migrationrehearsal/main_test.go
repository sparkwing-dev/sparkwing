package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the copy differs from the baseline only by what the upgrade retires.
func migratedPair(t *testing.T, extraMeta string) (before, after *db) {
	t.Helper()
	dir := t.TempDir()
	basePath := filepath.Join(dir, "before.db")
	st, err := store.Open(basePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	for _, stmt := range []string{
		`CREATE TABLE executors (name TEXT PRIMARY KEY, token_prefix TEXT NOT NULL)`,
		`INSERT INTO executors (name, token_prefix) VALUES ('helper', 'swr_helper00')`,
		`CREATE TABLE node_claim_offers (id TEXT PRIMARY KEY, executor TEXT NOT NULL)`,
		`INSERT INTO node_claim_offers (id, executor) VALUES ('offer-1', 'helper')`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"controller_authority_id", "bootstrap_latch", extraMeta} {
		if key == "" {
			continue
		}
		if _, err := st.DB().Exec(`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, 'v', ?)`, key, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	afterPath := filepath.Join(dir, "after.db")
	if err := os.WriteFile(afterPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if before, err = open("sqlite", basePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = before.Close() })
	if after, err = open("sqlite", afterPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = after.Close() })
	for _, stmt := range []string{
		`DROP TABLE node_claim_offers`,
		`DROP TABLE executors`,
		`DELETE FROM sparkwing_meta WHERE key IN ('controller_authority_id', 'bootstrap_latch')`,
	} {
		if _, err := after.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return before, after
}

func TestCompareAcceptsTheRowsTheUpgradeRetires(t *testing.T) {
	before, after := migratedPair(t, "")
	if err := compareStores(before, after, store.ExpectedSchemaVersion()); err != nil {
		t.Fatalf("compare after a correct upgrade = %v, want no failure", err)
	}
}

func TestCompareStillFailsOnAnyOtherLostMetadata(t *testing.T) {
	before, after := migratedPair(t, "operator_note")
	if _, err := after.Exec(`DELETE FROM sparkwing_meta WHERE key = 'operator_note'`); err != nil {
		t.Fatal(err)
	}
	if err := compareStores(before, after, store.ExpectedSchemaVersion()); err == nil {
		t.Fatal("compare passed although a metadata row the upgrade does not retire went missing")
	}
}

func TestRetiredLoginProbeNeedsTheRoutersOwn404(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken("canary", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	real := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(real.Close)
	var ok checker
	probeRetiredLogin(&ok, client{base: real.URL, auth: "Bearer " + raw})
	if ok.failures != 0 {
		t.Fatalf("probe against a controller without the route failed %d check(s)", ok.failures)
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		var c checker
		probeRetiredLogin(&c, client{base: fake.URL, auth: "Bearer " + raw})
		fake.Close()
		if c.failures != 1 {
			t.Errorf("probe answered %d recorded %d failure(s), want 1", status, c.failures)
		}
	}
}
