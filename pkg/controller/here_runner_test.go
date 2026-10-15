package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type hereFixture struct {
	t                     *testing.T
	url                   string
	st                    *store.Store
	editor, fleet, reader string
}

func newHereFixture(t *testing.T) *hereFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &hereFixture{t: t, st: st}
	now := time.Now().UTC()
	mint := func(principal, kind string, scopes []string) string {
		raw, _, err := st.CreateToken(principal, kind, scopes, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	f.editor = mint("dev@example.com", store.TokenKindUser, store.ScopesForRole(store.RoleEditor))
	f.reader = mint("viewer@example.com", store.TokenKindUser, store.ScopesForRole(store.RoleReader))
	f.fleet = mint("fleet", store.TokenKindRunner, store.MachineTokenScopes())
	f.restart()
	return f
}

// safety: a fresh controller has an empty credential cache, so a changed expiry is read from the store.
func (f *hereFixture) restart() {
	srv := httptest.NewServer(controller.New(f.st, nil).Handler())
	f.t.Cleanup(srv.Close)
	f.url = srv.URL
}

func (f *hereFixture) do(method, path, token string, body, out any) int {
	f.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.url+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

type hereTriggerResp struct {
	RunID                string `json:"run_id"`
	RunnerToken          string `json:"runner_token"`
	RunnerTokenExpiresAt int64  `json:"runner_token_expires_at"`
}

func (f *hereFixture) trigger(token string, extra map[string]any) (hereTriggerResp, int) {
	f.t.Helper()
	body := map[string]any{"pipeline": "hotfix", "trigger": map[string]any{"source": "cli"}}
	for k, v := range extra {
		body[k] = v
	}
	var out hereTriggerResp
	code := f.do("POST", "/api/v1/triggers", token, body, &out)
	return out, code
}

func (f *hereFixture) here() hereTriggerResp {
	f.t.Helper()
	out, code := f.trigger(f.editor, map[string]any{"runner": "here", "reason": "INC-42 fleet down"})
	if code != http.StatusAccepted || out.RunnerToken == "" {
		f.t.Fatalf("trigger --runner here = %d %+v, want 202 with a runner credential", code, out)
	}
	return out
}

// safety: the minted credential carries the runner's scopes only, names the
// run it is bound to, and expires with the hosted-run limit.
func TestHereRunner_TriggerMintsARunBoundExpiringCredential(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	tok, err := f.st.LookupTokenByPrefix(out.RunnerToken[:store.PrefixLen])
	if err != nil {
		t.Fatal(err)
	}
	if tok.Principal != store.HereRunnerPrincipalPrefix+out.RunID || tok.Kind != store.TokenKindRunner {
		t.Fatalf("credential = %s/%s, want a runner bound to %s", tok.Kind, tok.Principal, out.RunID)
	}
	for _, s := range tok.Scopes {
		if s != store.ScopeNodesClaim && s != store.ScopeTriggersClaim && s != store.ScopeRunsState {
			t.Fatalf("credential scopes = %v, want the machine scopes only", tok.Scopes)
		}
	}
	if tok.ExpiresAt == nil || tok.ExpiresAt.Sub(tok.CreatedAt) > 6*time.Hour || tok.ExpiresAt.Sub(tok.CreatedAt) < 6*time.Hour-time.Second ||
		tok.ExpiresAt.Unix() != out.RunnerTokenExpiresAt {
		t.Fatalf("credential expires %v (response %d), want six hours after the mint", tok.ExpiresAt, out.RunnerTokenExpiresAt)
	}
	events, err := f.st.ListEventsAfter(t.Context(), out.RunID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	for _, e := range events {
		if e.Kind == "runner_here" {
			if err := json.Unmarshal(e.Payload, &recorded); err != nil {
				t.Fatal(err)
			}
		}
	}
	if recorded["reason"] != "INC-42 fleet down" || recorded["by"] != "dev@example.com" || recorded["token_prefix"] != tok.Prefix {
		t.Fatalf("runner_here event = %v, want the reason, the caller and the credential", recorded)
	}
}

func TestHereRunner_TriggerRefusesWithoutAReasonOrRunWrite(t *testing.T) {
	f := newHereFixture(t)
	for name, extra := range map[string]map[string]any{
		"no reason":       {"runner": "here"},
		"blank reason":    {"runner": "here", "reason": "  "},
		"multi-line":      {"runner": "here", "reason": "a\nb"},
		"long reason":     {"runner": "here", "reason": strings.Repeat("x", 201)},
		"unknown runner":  {"runner": "label=prod", "reason": "x"},
		"reason alone":    {"reason": "x"},
		"with retry_of":   {"runner": "here", "reason": "x", "retry_of": "run-1"},
		"with parent run": {"runner": "here", "reason": "x", "parent_run_id": "run-1"},
	} {
		if _, code := f.trigger(f.editor, extra); code != http.StatusBadRequest && code != http.StatusForbidden {
			t.Errorf("%s: trigger = %d, want refused", name, code)
		}
	}
	if _, code := f.trigger(f.reader, map[string]any{"runner": "here", "reason": "x"}); code != http.StatusForbidden {
		t.Fatalf("a reader's runner here trigger = %d, want 403", code)
	}
}

// safety: a pinned trigger is invisible to the fleet's queue claim and refused
// to its by-id claim, so only the here-runner may host it.
func TestHereRunner_OnlyItsCredentialClaimsThePinnedRunAndOnlyOnce(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	if code := f.do("POST", "/api/v1/triggers/claim", f.fleet, map[string]any{}, nil); code != http.StatusNoContent {
		t.Fatalf("fleet queue claim = %d, want 204 with only a pinned trigger queued", code)
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", f.fleet, map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("fleet claim of the pinned run = %d, want 404", code)
	}
	var claimed store.Trigger
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, &claimed); code != http.StatusOK {
		t.Fatalf("here-runner claim = %d, want 200", code)
	}
	if claimed.ClaimToken == "" {
		t.Fatal("the here-runner's claim carries no claim token for the run's secrets")
	}
	if _, err := f.st.ReleaseClaimAtGeneration(t.Context(), out.RunID, claimed.ClaimSeq); err != nil {
		t.Fatal(err)
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("second here-runner claim = %d, want 404", code)
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", f.fleet, map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("fleet claim of the released pinned run = %d, want 404", code)
	}
}

// safety: the credential reaches its own run only; every other run, the queue
// claims, secrets by listing, and starting work answer 403.
func TestHereRunner_CredentialIsFencedToItsRun(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	other, code := f.trigger(f.editor, nil)
	if code != http.StatusAccepted {
		t.Fatalf("plain trigger = %d", code)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/triggers/claim"},
		{"POST", "/api/v1/nodes/claim"},
		{"POST", "/api/v1/triggers/" + other.RunID + "/claim"},
		{"GET", "/api/v1/runs/" + other.RunID},
		{"GET", "/api/v1/runs"},
		{"GET", "/api/v1/secrets"},
		{"POST", "/api/v1/triggers"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"pipeline": "hotfix", "trigger": map[string]any{"source": "cli"}}
		}
		if got := f.do(c.method, c.path, out.RunnerToken, body, nil); got != http.StatusForbidden {
			t.Errorf("%s %s with the here-runner credential = %d, want 403", c.method, c.path, got)
		}
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("here-runner claim = %d, want 200", code)
	}
	if got := f.do("GET", "/api/v1/runs/"+out.RunID, out.RunnerToken, nil, nil); got != http.StatusOK {
		t.Fatalf("GET its own claimed run = %d, want 200", got)
	}
	if code := f.do("POST", "/api/v1/triggers/claim", f.fleet, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("fleet claim of the unpinned trigger = %d, want 200", code)
	}
}

func TestHereRunner_CredentialExpires(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	ctx := context.Background()
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE tokens SET expires_at = ? WHERE prefix = ?`,
		time.Now().Add(-time.Minute).Unix(), out.RunnerToken[:store.PrefixLen]); err != nil {
		t.Fatal(err)
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, nil); code != http.StatusUnauthorized {
		t.Fatalf("expired here-runner claim = %d, want 401", code)
	}
}

// safety: a pipeline profile carries another pipeline's resource pins, so the
// credential reads only its own run's pipeline, and only while it holds the claim.
func TestHereRunner_ProfileReadsNeedItsOwnPipelineAndClaim(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	if code := f.do("GET", "/api/v1/pipelines/hotfix/profile", out.RunnerToken, nil, nil); code != http.StatusForbidden {
		t.Fatalf("own pipeline's profile before the claim = %d, want 403", code)
	}
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("here-runner claim = %d, want 200", code)
	}
	if code := f.do("GET", "/api/v1/pipelines/release/profile", out.RunnerToken, nil, nil); code != http.StatusForbidden {
		t.Fatalf("another pipeline's profile = %d, want 403", code)
	}
	if code := f.do("PUT", "/api/v1/pipelines/release/profile/pin", out.RunnerToken, map[string]any{}, nil); code != http.StatusForbidden {
		t.Fatalf("pin another pipeline's profile = %d, want 403", code)
	}
	if code := f.do("GET", "/api/v1/pipelines/hotfix/profile", out.RunnerToken, nil, nil); code == http.StatusForbidden {
		t.Fatalf("own pipeline's profile under the claim = %d, want it served", code)
	}
}

// safety: once claimed, the run lives under the hosted-run limit, so a run
// minted and claimed 90 minutes ago still renews its claim and reads its run.
func TestHereRunner_AClaimedRunOutlivesTheFirstHour(t *testing.T) {
	f := newHereFixture(t)
	out := f.here()
	var claimed store.Trigger
	if code := f.do("POST", "/api/v1/triggers/"+out.RunID+"/claim", out.RunnerToken, map[string]any{}, &claimed); code != http.StatusOK {
		t.Fatalf("here-runner claim = %d, want 200", code)
	}
	// safety: the clock moves 90 minutes by shifting the mint's own times back, so the stored lifetime decides.
	minted := time.Now().Add(-90 * time.Minute)
	shift := int64((90 * time.Minute).Seconds())
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE tokens SET created_at = created_at - ?, expires_at = expires_at - ? WHERE prefix = ?`,
		shift, shift, out.RunnerToken[:store.PrefixLen]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE triggers SET created_at = ? WHERE id = ?`,
		minted.UnixNano(), out.RunID); err != nil {
		t.Fatal(err)
	}
	f.restart()
	beat := client.NewWithToken(f.url, nil, out.RunnerToken)
	fenced := store.WithTriggerClaimFence(t.Context(), store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	if _, err := beat.HeartbeatTrigger(fenced, out.RunID); err != nil {
		t.Fatalf("heartbeat 90 minutes after the mint: %v", err)
	}
	if code := f.do("GET", "/api/v1/runs/"+out.RunID, out.RunnerToken, nil, nil); code != http.StatusOK {
		t.Fatalf("run read 90 minutes after the mint = %d, want 200", code)
	}
}
