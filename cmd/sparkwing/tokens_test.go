package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func fixedTokens() []controllerwire.TokenMetadata {
	last := int64(1714867200)
	revoked := int64(1714953600)
	return []controllerwire.TokenMetadata{
		{
			Prefix:     "swk_abcd",
			Kind:       "runner",
			Principal:  "runner:sparkwing-warm-runner",
			Scopes:     []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.LogsRead},
			LastUsedAt: &last,
		},
		{
			Prefix:    "swk_admn",
			Kind:      "member",
			Principal: "user:admin",
			Scopes:    []capability.Grant{capability.TeamAdmin},
		},
		{
			Prefix:     "swk_dead",
			Kind:       "runner",
			Principal:  "deploy-bot",
			Scopes:     []capability.Grant{capability.RunsWrite},
			LastUsedAt: &last,
			RevokedAt:  &revoked,
		},
		{
			Prefix:    "swk_void",
			Kind:      "runner",
			Principal: "scopeless-bot",
			Scopes:    nil,
		},
	}
}

func TestRenderTokensTable_IncludesScopesColumn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTokenMetadata(&buf, fixedTokens(), "pretty"); err != nil {
		t.Fatalf("renderTokensTable: %v", err)
	}
	out := buf.String()

	header := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"PREFIX", "TYPE", "PRINCIPAL", "SCOPES", "LAST USED"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q missing column %q", header, want)
		}
	}

	if !strings.Contains(out, "nodes.claim,triggers.claim,logs.read") {
		t.Errorf("table missing warm-runner scopes:\n%s", out)
	}
	if !strings.Contains(out, "user:admin") {
		t.Errorf("table missing user:admin row:\n%s", out)
	}
	if !strings.Contains(out, "team.admin") || strings.Contains(out, "*") {
		t.Errorf("scope changed: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "scopeless-bot") {
			if !strings.Contains(line, " - ") && !strings.Contains(line, "\t-\t") {
				t.Errorf("scopeless row should show '-' placeholder, got: %q", line)
			}
		}
	}
	if !strings.Contains(out, "2024-05-06") {
		t.Errorf("revoked row missing revocation time:\n%s", out)
	}
}

func TestRenderTokensTable_EmptyList(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTokenMetadata(&buf, nil, "pretty"); err != nil {
		t.Fatalf("renderTokensTable(nil): %v", err)
	}
	if got := strings.TrimSpace(buf.String()); !strings.Contains(got, "PREFIX") {
		t.Errorf("empty table = %q, want a column header", got)
	}
}

func TestRenderTokensJSON_ExposesScopesArray(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTokenMetadata(&buf, fixedTokens(), "json"); err != nil {
		t.Fatalf("renderTokensJSON: %v", err)
	}

	got := decodeNDJSON[map[string]any](t, buf.String())
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
	wantScopes := []string{"nodes.claim", "triggers.claim", "logs.read"}
	for i, s := range wantScopes {
		if scopes[i] != s {
			t.Errorf("scopes[%d] = %v, want %s", i, scopes[i], s)
		}
	}

	admin := got[1]
	adminScopes, _ := admin["scopes"].([]any)
	if len(adminScopes) != 1 || adminScopes[0] != "team.admin" {
		t.Errorf("admin scopes = %v, want [team.admin]", adminScopes)
	}
}

func TestRenderTokensJSON_NilTokensEmitsAnEmptyStream(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTokenMetadata(&buf, nil, "json"); err != nil {
		t.Fatalf("renderTokensJSON(nil): %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("renderTokensJSON(nil) = %q, want an empty stream", got)
	}
}
