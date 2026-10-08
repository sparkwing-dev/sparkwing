package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunDocsMigrations_ListTableMentionsKnownVersion(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"--output", "pretty"}); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("list output missing v0.4.0 row; got:\n%s", out)
	}
	if !strings.Contains(out, "VERSION") {
		t.Errorf("list output missing VERSION header; got:\n%s", out)
	}
}

func TestRunDocsMigrations_ListJSONIsParseable(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"-o", "json"}); err != nil {
			t.Fatalf("list -o json: %v", err)
		}
	})
	rows := decodeNDJSON[struct {
		Version string `json:"version"`
		Slug    string `json:"slug"`
		Bytes   int    `json:"bytes"`
	}](t, out)
	if len(rows) == 0 {
		t.Fatal("expected at least one row in json output")
	}
	for _, r := range rows {
		if r.Slug != r.Version {
			t.Errorf("row %+v: slug should equal version (matches web /migrations/index.json)", r)
		}
		if r.Bytes <= 0 {
			t.Errorf("row %+v has non-positive bytes", r)
		}
	}
}

func TestRunDocsMigrations_ListJSONSchemaMatchesWeb(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"-o", "json"}); err != nil {
			t.Fatalf("list -o json: %v", err)
		}
	})
	rows := decodeNDJSON[map[string]json.RawMessage](t, out)
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}
	wantKeys := []string{"version", "slug", "title", "date", "summary", "bytes"}
	row := rows[0]
	if len(row) != len(wantKeys) {
		t.Errorf("row has %d keys, want %d (%v); got %v", len(row), len(wantKeys), wantKeys, keysOf(row))
	}
	for _, k := range wantKeys {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing key %q (web schema requires it)", k)
		}
	}
	gotOrder := keysInOrder(out)
	if len(gotOrder) != len(wantKeys) {
		t.Errorf("ordered keys = %v; want %v", gotOrder, wantKeys)
		return
	}
	for i := range wantKeys {
		if gotOrder[i] != wantKeys[i] {
			t.Errorf("ordered key[%d] = %q; want %q (web schema order: %v)", i, gotOrder[i], wantKeys[i], wantKeys)
		}
	}
}

func TestRunDocsList_JSONSchemaMatchesWeb(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsList([]string{"-o", "json"}); err != nil {
			t.Fatalf("list -o json: %v", err)
		}
	})
	rows := decodeNDJSON[map[string]json.RawMessage](t, out)
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}
	wantKeys := []string{"slug", "title", "summary", "bytes"}
	row := rows[0]
	if len(row) != len(wantKeys) {
		t.Errorf("row has %d keys, want %d (%v); got %v", len(row), len(wantKeys), wantKeys, keysOf(row))
	}
	for _, k := range wantKeys {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing key %q (web schema requires it)", k)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysInOrder(ndjson string) []string {
	dec := json.NewDecoder(strings.NewReader(ndjson))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil
		}
		keys = append(keys, key)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil
		}
	}
	return keys
}

func TestRunDocsMigrations_ListPlainOnePerLine(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"-o", "plain"}); err != nil {
			t.Fatalf("list -o plain: %v", err)
		}
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "v") {
			t.Errorf("expected version-per-line; got %q", line)
		}
	}
}

func TestRunDocsMigrations_VersionPrintsBody(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"--version", "v0.4.0"}); err != nil {
			t.Fatalf("read: %v", err)
		}
	})
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("read output missing H1; got:\n%s", out[:min(400, len(out))])
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("read output should end with newline")
	}
}

func TestRunDocsMigrations_VersionAcceptsPositionalFallback(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"v0.4.0"}); err != nil {
			t.Fatalf("read v0.4.0 (positional): %v", err)
		}
	})
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("positional fallback didn't read v0.4.0")
	}
}

func TestRunDocsMigrations_VersionUnknownVersionSuggestsList(t *testing.T) {
	err := runDocsMigrations([]string{"--version", "v9.9.9"})
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	if !strings.Contains(err.Error(), "available versions") {
		t.Errorf("error should suggest available versions; got %v", err)
	}
}

func TestRunDocsMigrations_VersionRejectsBadSemver(t *testing.T) {
	err := runDocsMigrations([]string{"--version", "garbage"})
	if err == nil {
		t.Fatal("expected error for invalid semver")
	}
}

func TestRunDocsMigrations_VersionAndRangeAreExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "v0.4.0", "--from", "v0.3.0"},
		{"v0.4.0", "--to", "v0.4.0"},
	} {
		if err := runDocsMigrations(args); err == nil {
			t.Errorf("docs migrations %v was accepted", args)
		}
	}
}

func TestRunDocsMigrations_RangeRangeHeaderAndBody(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"--from", "v0.3.0", "--to", "v0.4.0", "--output", "plain"}); err != nil {
			t.Fatalf("between: %v", err)
		}
	})
	if !strings.Contains(out, "# Migration: v0.3.0 -> v0.4.0") {
		t.Errorf("missing range header; got prefix:\n%s", out[:min(200, len(out))])
	}
	if !strings.Contains(out, "---") {
		t.Errorf("missing markdown separator")
	}
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("missing v0.4.0 body in concatenation")
	}
}

func TestRunDocsMigrations_RangeDefaultsWork(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"--from", "v0.0.0", "--output", "plain"}); err != nil {
			t.Fatalf("between (no args): %v", err)
		}
	})
	if !strings.Contains(out, "# Migration: v0.0.0 ->") {
		t.Errorf("expected default --from v0.0.0; got prefix:\n%s", out[:min(200, len(out))])
	}
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("expected default --to to include v0.4.0")
	}
}

func TestRunDocsMigrations_RangeRejectsBadSemver(t *testing.T) {
	if err := runDocsMigrations([]string{"--from", "garbage"}); err == nil {
		t.Error("expected error for invalid --from")
	}
}

func TestRunDocsMigrations_RemovedVerbsFailLoudly(t *testing.T) {
	err := runDocsMigrations([]string{"between"})
	if err == nil || !strings.Contains(err.Error(), "not a valid semver") {
		t.Fatalf("expected an invalid-version error; got %v", err)
	}
	for _, args := range [][]string{
		{"read", "--version", "v0.4.0"},
		{"between", "--from", "v0.3.0", "--to", "v0.4.0"},
	} {
		if err := runDocsMigrations(args); err == nil || !strings.Contains(err.Error(), "unexpected positional") {
			t.Errorf("docs migrations %v: err = %v", args, err)
		}
	}
}

func TestRunDocsMigrations_NoFlagsLists(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrations([]string{"-o", "plain"}); err != nil {
			t.Fatalf("docs migrations: %v", err)
		}
	})
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("bare docs migrations did not list; got %q", out)
	}
}

func TestRunDocs_DispatchesMigrationsVerb(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocs([]string{"migrations", "-o", "plain"}); err != nil {
			t.Fatalf("runDocs migrations: %v", err)
		}
	})
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("docs dispatcher didn't route to migrations; got %q", out)
	}
}
