package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunDocsMigrationsList_TableMentionsKnownVersion(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsList(nil); err != nil {
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

func TestRunDocsMigrationsList_JSONIsParseable(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsList([]string{"-o", "json"}); err != nil {
			t.Fatalf("list -o json: %v", err)
		}
	})
	var rows []struct {
		Version string `json:"version"`
		Slug    string `json:"slug"`
		Bytes   int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one row in json output")
	}
	for _, r := range rows {
		if r.Slug != "migrations/"+r.Version {
			t.Errorf("row %+v slug doesn't match version", r)
		}
		if r.Bytes <= 0 {
			t.Errorf("row %+v has non-positive bytes", r)
		}
	}
}

func TestRunDocsMigrationsList_PlainOnePerLine(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsList([]string{"-o", "plain"}); err != nil {
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

func TestRunDocsMigrationsRead_PrintsBody(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsRead([]string{"--version", "v0.4.0"}); err != nil {
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

func TestRunDocsMigrationsRead_AcceptsPositionalFallback(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsRead([]string{"v0.4.0"}); err != nil {
			t.Fatalf("read v0.4.0 (positional): %v", err)
		}
	})
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("positional fallback didn't read v0.4.0")
	}
}

func TestRunDocsMigrationsRead_UnknownVersionSuggestsList(t *testing.T) {
	err := runDocsMigrationsRead([]string{"--version", "v9.9.9"})
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	if !strings.Contains(err.Error(), "available versions") {
		t.Errorf("error should suggest available versions; got %v", err)
	}
}

func TestRunDocsMigrationsRead_RejectsBadSemver(t *testing.T) {
	err := runDocsMigrationsRead([]string{"--version", "garbage"})
	if err == nil {
		t.Fatal("expected error for invalid semver")
	}
}

func TestRunDocsMigrationsRead_RequiresVersion(t *testing.T) {
	err := runDocsMigrationsRead(nil)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected --version required error; got %v", err)
	}
}

func TestRunDocsMigrationsBetween_RangeHeaderAndBody(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsBetween([]string{"--from", "v0.3.0", "--to", "v0.4.0"}); err != nil {
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

func TestRunDocsMigrationsBetween_DefaultsWork(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocsMigrationsBetween(nil); err != nil {
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

func TestRunDocsMigrationsBetween_RejectsBadSemver(t *testing.T) {
	if err := runDocsMigrationsBetween([]string{"--from", "garbage"}); err == nil {
		t.Error("expected error for invalid --from")
	}
}

func TestRunDocsMigrations_DispatcherRejectsUnknownVerb(t *testing.T) {
	err := runDocsMigrations([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown verb") {
		t.Fatalf("expected unknown-verb error; got %v", err)
	}
}

func TestRunDocsMigrations_DispatcherRequiresSubcommand(t *testing.T) {
	err := runDocsMigrations(nil)
	if err == nil || !strings.Contains(err.Error(), "missing subcommand") {
		t.Fatalf("expected missing-subcommand error; got %v", err)
	}
}

func TestRunDocs_DispatchesMigrationsVerb(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runDocs([]string{"migrations", "list", "-o", "plain"}); err != nil {
			t.Fatalf("runDocs migrations: %v", err)
		}
	})
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("docs dispatcher didn't route to migrations; got %q", out)
	}
}
