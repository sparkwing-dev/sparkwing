package controller_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func auditFixture(t *testing.T) (*identityFixture, *lockedBuffer) {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	logs := &lockedBuffer{}
	f := newIdentityFixtureWith(t, fixtureOpts{
		license: raw, key: pub, logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	return f, logs
}

// A write is logged with who made it, for which team, the route it reached
// and the secret it names; its query and its bearer token are not in the log.
func TestAuditRecordNamesThePrincipalAndNeverTheRawPath(t *testing.T) {
	f, logs := auditFixture(t)
	owner := f.user("o", "olga@example.com")
	const secretName = "PROD_DB_PASSWORD_ROTATED"

	req, err := http.NewRequest(http.MethodDelete, f.url+"/api/v1/secrets/"+secretName+"?reason=leak-me-query", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", owner.auth)
	req.Header.Set("X-Request-Id", "req-audit-1")
	req.Header.Set("User-Agent", "agent olga@example.com swk_leakedtoken123 "+strings.Repeat("u", 300))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "req-audit-1" {
		t.Fatalf("echoed request id = %q, want the caller's", got)
	}
	f.call("POST", "/api/v1/tokens", "Bearer "+f.admin, map[string]any{
		"principal": "svc", "kind": "service", "scopes": []string{"runs.read"},
	}, nil)
	f.call("PUT", "/api/v1/teams/"+owner.team+"/repos/Acme/Widgets/dispatch", "Bearer "+f.admin,
		map[string]string{"dispatch": "controller"}, nil)

	var deleted, minted, optIn map[string]any
	for _, rec := range logs.records(t, "audit") {
		switch rec["route"] {
		case "/api/v1/secrets/{name}":
			deleted = rec
		case "/api/v1/tokens":
			minted = rec
		case "/api/v1/teams/{team}/repos/{owner}/{name}/dispatch":
			optIn = rec
		}
	}
	if optIn == nil || optIn["repo_owner"] != "Acme" || optIn["repo_name"] != "Widgets" ||
		optIn["target_team"] != owner.team || optIn["status"] != float64(http.StatusOK) ||
		!strings.HasPrefix(f.admin, optIn["principal_id"].(string)) {
		t.Errorf("opt-in audit record = %v, want the repository, its team and the admin token", optIn)
	}
	if deleted["repo_name"] != nil {
		t.Errorf("a secret route's {name} reached the record as a repository: %v", deleted)
	}
	if deleted == nil || minted == nil {
		t.Fatalf("audit records missing: %s", logs.String())
	}
	if deleted["secret_name"] != secretName {
		t.Errorf("secret delete audit secret_name = %v, want %s", deleted["secret_name"], secretName)
	}
	for key, want := range map[string]any{
		"request_id": "req-audit-1", "method": "DELETE", "principal_kind": store.TokenKindUser,
		"team": owner.team, "principal_id": owner.id, "client_class": "other",
	} {
		if deleted[key] != want {
			t.Errorf("audit %s = %v, want %v", key, deleted[key], want)
		}
	}
	for _, key := range []string{"client_ip", "status", "dur_ms", "ts"} {
		if deleted[key] == nil || deleted[key] == "" {
			t.Errorf("audit record has no %s: %v", key, deleted)
		}
	}
	if prefix, _ := minted["principal_id"].(string); prefix == "" || !strings.HasPrefix(f.admin, prefix) {
		t.Errorf("token-authenticated audit principal = %v, want the token's prefix", minted["principal_id"])
	}
	all := logs.String()
	for _, leak := range []string{"leak-me-query", f.admin, strings.TrimPrefix(owner.auth, "Session "), "olga@example.com", "swk_leakedtoken123", "uuuu"} {
		if strings.Contains(all, leak) {
			t.Errorf("log carries %q:\n%s", leak, all)
		}
	}
}

// An empty claim poll is not audited, a read logs its route and never its
// path, and a request id that is not a short token is replaced.
func TestAuditSkipsEmptyClaimPollsAndReadsLogRoutes(t *testing.T) {
	f, logs := auditFixture(t)
	owner := f.user("o", "olga@example.com")
	runner := "Bearer " + mintRunner(f, owner.auth, "pool").Token
	if code := f.call("POST", "/api/v1/triggers/claim", runner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("empty trigger claim = %d, want 204", code)
	}
	if code := f.call("GET", "/api/v1/runs/run-with-a-name", owner.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("missing run = %d, want 404", code)
	}
	req, err := http.NewRequest(http.MethodGet, f.url+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "bad id; drop")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got == "" || strings.Contains(got, "bad") {
		t.Fatalf("request id for an invalid caller value = %q, want a generated one", got)
	}

	for _, rec := range logs.records(t, "audit") {
		if strings.HasSuffix(rec["route"].(string), "/claim") {
			t.Errorf("empty claim poll audited: %v", rec)
		}
	}
	var read map[string]any
	for _, rec := range logs.records(t, "http") {
		if rec["route"] == "/api/v1/runs/{id}" {
			read = rec
		}
	}
	if read == nil || read["path"] != nil {
		t.Fatalf("read log = %v, want the route and no path", read)
	}
	if strings.Contains(logs.String(), "run-with-a-name") {
		t.Errorf("read log carries the raw path: %s", logs.String())
	}
}

// A write addressing one run and node logs those ids from the path.
func TestAuditRecordCarriesAllowListedPathIDs(t *testing.T) {
	f, logs := auditFixture(t)
	owner := f.user("o", "olga@example.com")
	f.call("POST", "/api/v1/runs/run-7/nodes/build/finish", owner.auth, map[string]any{}, nil)
	for _, rec := range logs.records(t, "audit") {
		if rec["route"] == "/api/v1/runs/{id}/nodes/{nodeID}/finish" {
			if rec["run_id"] != "run-7" || rec["node_id"] != "build" {
				t.Fatalf("path ids = %v %v, want run-7 build", rec["run_id"], rec["node_id"])
			}
			return
		}
	}
	t.Fatalf("no audit record for the node finish: %s", logs.String())
}

func auditFor(t *testing.T, logs *lockedBuffer, method, route string) map[string]any {
	t.Helper()
	var last map[string]any
	for _, rec := range logs.records(t, "audit") {
		if rec["method"] == method && rec["route"] == route {
			last = rec
		}
	}
	if last == nil {
		t.Fatalf("no audit record for %s %s: %s", method, route, logs.String())
	}
	return last
}

// Reads that hand out a secret or a credential list, identity changes and
// refused requests are audited with the caller and the target, so a leak or
// a hostile member can be traced; the bearer itself never reaches the log.
func TestAuditNamesReadersTargetsAndDeniedSubjects(t *testing.T) {
	f, logs := auditFixture(t)
	owner := f.user("o", "olga@example.com")
	member := f.user("m", "mia@example.com")
	f.join(owner, member, "mia@example.com", "reader")
	if code := f.call("POST", "/api/v1/secrets", owner.auth, map[string]any{"name": "DEPLOY_KEY", "value": "s3cr3t-value", "masked": false}, nil); code != http.StatusNoContent {
		t.Fatalf("create secret = %d", code)
	}
	if code := f.call("GET", "/api/v1/secrets/DEPLOY_KEY", owner.auth, nil, nil); code != http.StatusOK {
		t.Fatalf("read secret = %d", code)
	}
	f.call("GET", "/api/v1/team/cli-tokens", owner.auth, nil, nil)
	if code := f.call("PATCH", "/api/v1/team/members/"+member.id, owner.auth, map[string]string{"role": "editor"}, nil); code != http.StatusNoContent {
		t.Fatalf("set role = %d", code)
	}
	invitation := f.invite(owner, "nia@example.com", "reader")
	if code := f.call("DELETE", "/api/v1/team/invitations/"+invitation, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("withdraw invitation = %d", code)
	}
	runner := mintRunner(f, owner.auth, "pool")
	if code := f.call("DELETE", "/api/v1/team/runner-tokens/"+runner.Prefix, owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke runner token = %d", code)
	}
	forged := runner.Token[:len(runner.Token)-4] + "zzzz"
	if code := f.call("GET", "/api/v1/runs", "Bearer "+forged, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("forged bearer = %d, want 401", code)
	}
	if code := f.call("GET", "/api/v1/team/invitations", member.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("editor listing invitations = %d, want 403", code)
	}

	for _, tc := range []struct {
		method, route string
		want          map[string]any
	}{
		{"GET", "/api/v1/secrets/{name}", map[string]any{"principal_id": owner.id, "team": owner.team, "secret_name": "DEPLOY_KEY"}},
		{"POST", "/api/v1/secrets", map[string]any{"principal_id": owner.id, "secret_name": "DEPLOY_KEY"}},
		{"GET", "/api/v1/team/cli-tokens", map[string]any{"principal_id": owner.id, "team": owner.team}},
		{"PATCH", "/api/v1/team/members/{user_id}", map[string]any{"principal_id": owner.id, "member_id": member.id, "role": "editor"}},
		{"POST", "/api/v1/team/invitations", map[string]any{"invitation_id": invitation, "role": "reader"}},
		{"DELETE", "/api/v1/team/invitations/{id}", map[string]any{"invitation_id": invitation}},
		{"DELETE", "/api/v1/team/runner-tokens/{prefix}", map[string]any{"token_prefix": runner.Prefix}},
		{"GET", "/api/v1/runs", map[string]any{"status": float64(http.StatusUnauthorized), "attempted_prefix": runner.Prefix}},
		{"GET", "/api/v1/team/invitations", map[string]any{"status": float64(http.StatusForbidden), "principal_id": member.id}},
	} {
		rec := auditFor(t, logs, tc.method, tc.route)
		if rec["client_ip"] == nil || rec["client_ip"] == "" {
			t.Errorf("%s %s: no client_ip: %v", tc.method, tc.route, rec)
		}
		for key, want := range tc.want {
			if rec[key] != want {
				t.Errorf("%s %s: %s = %v, want %v", tc.method, tc.route, key, rec[key], want)
			}
		}
	}
	all := logs.String()
	for _, leak := range []string{"s3cr3t-value", forged, runner.Token} {
		if strings.Contains(all, leak) {
			t.Errorf("log carries %q", leak)
		}
	}
}
