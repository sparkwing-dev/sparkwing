package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The cache honors a grant on its signature alone, so a runner token gets one
// only through a live claim on the run. Otherwise a grant minted just before
// its minter's removal would keep the team's cache open for its whole life.
func TestCacheGrantRefusesARunnerTokenWithoutALiveClaim(t *testing.T) {
	f := newTenancyFixture(t, openSQLiteStore(t))
	f.srv.WithCacheGrantKey("cache-grant-key-distinct-from-operator-token")

	code, raw := f.do(http.MethodPost, "/api/v1/team/runner-tokens", f.editorA,
		map[string]any{"name": "dave-box", "repos": []string{"github.com/acme/*"}})
	if code != http.StatusCreated {
		t.Fatalf("mint runner token: %d %s", code, raw)
	}
	var minted struct{ Token string }
	if err := json.Unmarshal([]byte(raw), &minted); err != nil {
		t.Fatal(err)
	}

	code, raw = f.do(http.MethodPost, "/api/v1/runs/"+f.runA+"/cache-grant", "Bearer "+minted.Token, nil)
	if code != http.StatusForbidden || !strings.Contains(raw, "claim_required") {
		t.Fatalf("cache grant with no claim = %d %s, want 403 claim_required", code, raw)
	}
}
