package controller_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

func createTokenBody(principal string, scopes []string) map[string]any {
	return map[string]any{
		"principal": principal,
		"kind":      "user",
		"scopes":    scopes,
	}
}

func decodeErrorMessage(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return out.Error
}

func TestCreateToken_UnknownScopeRejected(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

	status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
		createTokenBody("typo-bot", []string{"jobs:read"}))
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", status, body)
	}
	msg := decodeErrorMessage(t, body)
	if !strings.Contains(msg, `"jobs:read"`) {
		t.Errorf("error %q does not name the offending scope", msg)
	}
	for _, want := range []string{"runs.read", "nodes.claim", "approvals.write", "admin"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not list valid scope %q", msg, want)
		}
	}

	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("rejected request minted %d token(s); want none", len(tokens))
	}
}

func TestCreateToken_MixedValidAndUnknownRejected(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

	status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
		createTokenBody("half-right", []string{"runs.read", "jobs:write", "runs.admin"}))
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", status, body)
	}
	msg := decodeErrorMessage(t, body)
	for _, want := range []string{`"jobs:write"`, `"runs.admin"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name offender %s", msg, want)
		}
	}
	if !strings.Contains(msg, "unknown scopes") {
		t.Errorf("error %q does not pluralize for two offenders", msg)
	}

	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("rejected request minted %d token(s); want none", len(tokens))
	}
}

func TestCreateToken_ValidScopesAccepted(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	want := []string{"runs.read", "runs.write", "logs.write", "admin"}
	status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
		createTokenBody("deploy-bot", want))
	if status != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201", status, body)
	}
	var out struct {
		Token    string `json:"token"`
		Metadata struct {
			Scopes []string `json:"scopes"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if out.Token == "" {
		t.Errorf("response carried no raw token: %s", body)
	}
	if strings.Join(out.Metadata.Scopes, ",") != strings.Join(want, ",") {
		t.Errorf("scopes=%v want %v", out.Metadata.Scopes, want)
	}
}

func TestCreateToken_EmptyScopesAccepted(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	for name, body := range map[string]map[string]any{
		"absent": {"principal": "inert", "kind": "user"},
		"empty":  createTokenBody("inert-too", []string{}),
	} {
		status, resp := postJSONWithStatus(t, base+"/api/v1/tokens", body)
		if status != http.StatusCreated {
			t.Errorf("%s scopes: status=%d body=%s, want 201", name, status, resp)
			continue
		}
		var out struct {
			Metadata struct {
				Scopes []string `json:"scopes"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(resp), &out); err != nil {
			t.Errorf("%s scopes: decode %q: %v", name, resp, err)
			continue
		}
		if len(out.Metadata.Scopes) != 0 {
			t.Errorf("%s scopes: got %v, want none", name, out.Metadata.Scopes)
		}
	}
}

func TestCreateToken_BlankAndPaddedScopesTolerated(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
		createTokenBody("padded", []string{" runs.read ", "", "admin"}))
	if status != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201", status, body)
	}
	var out struct {
		Metadata struct {
			Scopes []string `json:"scopes"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if strings.Join(out.Metadata.Scopes, ",") != "runs.read,admin" {
		t.Errorf("scopes=%v want [runs.read admin]", out.Metadata.Scopes)
	}
}

var scopeConstRE = regexp.MustCompile(`\bScope\w*\s*=\s*"([a-z][a-z.]*)"`)

func TestCreateToken_AllowlistCoversEveryScopeConstant(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatalf("read auth.go: %v", err)
	}
	matches := scopeConstRE.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("no Scope* constants found in auth.go")
	}

	status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
		createTokenBody("drift-probe", []string{"definitely.not.a.scope"}))
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", status, body)
	}
	msg := decodeErrorMessage(t, body)
	for _, m := range matches {
		if !strings.Contains(msg, m[1]) {
			t.Errorf("scope constant %q missing from the creation allowlist: %s", m[1], msg)
		}
	}

	for _, m := range matches {
		status, body := postJSONWithStatus(t, base+"/api/v1/tokens",
			createTokenBody("drift-probe-"+m[1], []string{m[1]}))
		if status != http.StatusCreated {
			t.Errorf("scope %q rejected at creation: status=%d body=%s", m[1], status, body)
		}
	}
}
