package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// IMP-024: `cluster tokens list` surfaces token scopes (canonical
// diagnostic for the IMP-002 root cause: warm-runner's mounted token
// missed `logs.write`). These tests pin the formatter +
// table/JSON renderers so a future refactor can't silently drop the
// SCOPES column or the admin "*" collapse.

func TestFormatScopes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty renders as dash", nil, "-"},
		{"single scope", []string{"logs.write"}, "logs.write"},
		{"multiple scopes joined with comma", []string{"nodes.claim", "triggers.claim", "logs.write"}, "nodes.claim,triggers.claim,logs.write"},
		{"admin collapses to star", []string{"admin"}, "*"},
		{"admin alongside others still collapses", []string{"runs.read", "admin", "logs.write"}, "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatScopes(tc.in)
			if got != tc.want {
				t.Fatalf("formatScopes(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// fixedTokens is the canonical fixture used across the renderer tests
// so the table + JSON expectations stay aligned.
func fixedTokens() []tokenListItem {
	last := int64(1714867200) // 2024-05-05 00:00:00 UTC, stable across machines
	revoked := int64(1714953600)
	return []tokenListItem{
		{
			Prefix:     "swu_abcd",
			Kind:       "runner",
			Principal:  "agent:sparkwing-warm-runner",
			Scopes:     []string{"nodes.claim", "triggers.claim", "logs.write"},
			LastUsedAt: &last,
		},
		{
			Prefix:    "swu_admn",
			Kind:      "user",
			Principal: "user:admin",
			Scopes:    []string{"admin"},
		},
		{
			Prefix:     "swu_dead",
			Kind:       "service",
			Principal:  "deploy-bot",
			Scopes:     []string{"runs.write"},
			LastUsedAt: &last,
			RevokedAt:  &revoked,
		},
		{
			Prefix:    "swu_void",
			Kind:      "service",
			Principal: "scopeless-bot",
			Scopes:    nil,
		},
	}
}

func TestRenderTokensTable_IncludesScopesColumn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderTokensTable(&buf, fixedTokens()); err != nil {
		t.Fatalf("renderTokensTable: %v", err)
	}
	out := buf.String()

	// Header has the SCOPES column.
	header := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"PREFIX", "TYPE", "PRINCIPAL", "SCOPES", "LAST_USED"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q missing column %q", header, want)
		}
	}

	// Warm-runner row enumerates the diagnostic-relevant scopes
	// in the order they came back from the controller.
	if !strings.Contains(out, "nodes.claim,triggers.claim,logs.write") {
		t.Errorf("table missing warm-runner scopes:\n%s", out)
	}
	// Admin token collapses to "*" rather than echoing the literal
	// scope name.
	if !strings.Contains(out, "user:admin") {
		t.Errorf("table missing user:admin row:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "user:admin") {
			if !strings.Contains(line, "*") {
				t.Errorf("admin row should render scopes as *, got: %q", line)
			}
			if strings.Contains(line, "admin,") || strings.HasSuffix(strings.TrimSpace(line), "admin") {
				t.Errorf("admin row should not enumerate the literal scope name, got: %q", line)
			}
		}
	}
	// Empty scope set renders as "-" not blank.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "scopeless-bot") {
			if !strings.Contains(line, " - ") && !strings.Contains(line, "\t-\t") {
				t.Errorf("scopeless row should show '-' placeholder, got: %q", line)
			}
		}
	}
	// Revoked tokens annotate LAST_USED so operators can spot them.
	if !strings.Contains(out, "(revoked)") {
		t.Errorf("revoked row missing (revoked) marker:\n%s", out)
	}
}

func TestRenderTokensTable_EmptyList(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderTokensTable(&buf, nil); err != nil {
		t.Fatalf("renderTokensTable(nil): %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "(no tokens)" {
		t.Errorf("empty table = %q, want \"(no tokens)\"", got)
	}
}

func TestRenderTokensJSON_ExposesScopesArray(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderTokensJSON(&buf, fixedTokens()); err != nil {
		t.Fatalf("renderTokensJSON: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 4 {
		t.Fatalf("got %d tokens, want 4", len(got))
	}

	warm := got[0]
	scopes, ok := warm["scopes"].([]any)
	if !ok {
		t.Fatalf("scopes field is not an array: %T (%v)", warm["scopes"], warm["scopes"])
	}
	if len(scopes) != 3 {
		t.Errorf("warm-runner scopes length = %d, want 3", len(scopes))
	}
	wantScopes := []string{"nodes.claim", "triggers.claim", "logs.write"}
	for i, s := range wantScopes {
		if scopes[i] != s {
			t.Errorf("scopes[%d] = %v, want %s", i, scopes[i], s)
		}
	}

	// JSON keeps the literal "admin" scope so callers can do their
	// own policy logic; only the table view collapses it to "*".
	admin := got[1]
	adminScopes, _ := admin["scopes"].([]any)
	if len(adminScopes) != 1 || adminScopes[0] != "admin" {
		t.Errorf("admin scopes = %v, want [admin] (JSON should not collapse)", adminScopes)
	}
}

func TestRenderTokensJSON_NilTokensEmitsEmptyArray(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderTokensJSON(&buf, nil); err != nil {
		t.Fatalf("renderTokensJSON(nil): %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("renderTokensJSON(nil) = %q, want \"[]\"", got)
	}
}
