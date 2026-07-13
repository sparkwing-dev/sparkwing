package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestVersionTransition(t *testing.T) {
	cases := []struct {
		prev, cur string
		want      bool
	}{
		{"v0.15.0", "v0.16.0", true},
		{"v0.15.0", "v0.15.0", false},
		{"", "v0.16.0", false},
		{"v0.15.0", "", false},
		{"(unknown)", "v0.16.0", false},
		{"v0.15.0", "(unknown)", false},
	}
	for _, c := range cases {
		if got := versionTransition(c.prev, c.cur); got != c.want {
			t.Errorf("versionTransition(%q, %q) = %v, want %v", c.prev, c.cur, got, c.want)
		}
	}
}

// TestNoteVersionTransition_OnceOnly is the core once-per-transition
// contract: the first invocation after the stamped version differs
// emits exactly one line and rewrites the stamp; the next invocation is
// silent.
func TestNoteVersionTransition_OnceOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)
	if err := os.WriteFile(p.LastVersionFile(), []byte("v0.14.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	noteVersionTransition(&first, "version")
	if !strings.Contains(first.String(), "sparkwing upgraded v0.14.0 -> ") {
		t.Fatalf("first run did not emit the upgrade line; got %q", first.String())
	}
	if !strings.Contains(first.String(), "--topic changelog") {
		t.Fatalf("upgrade line missing the changelog pointer; got %q", first.String())
	}

	stamp, _ := os.ReadFile(p.LastVersionFile())
	if strings.TrimSpace(string(stamp)) != installedVersion() {
		t.Fatalf("stamp = %q, want %q", strings.TrimSpace(string(stamp)), installedVersion())
	}

	var second bytes.Buffer
	noteVersionTransition(&second, "version")
	if second.Len() != 0 {
		t.Fatalf("second run should be silent; got %q", second.String())
	}
}

// TestNoteVersionTransition_InfoSuppressesStderr verifies the info verb
// stashes the notice for inline rendering instead of duplicating it on
// stderr.
func TestNoteVersionTransition_InfoSuppressesStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)
	if err := os.WriteFile(p.LastVersionFile(), []byte("v0.14.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	noteVersionTransition(&buf, "info")
	if buf.Len() != 0 {
		t.Fatalf("info verb should not write the line to the stream; got %q", buf.String())
	}
	if !strings.Contains(pendingUpgradeNotice, "sparkwing upgraded v0.14.0 -> ") {
		t.Fatalf("info verb did not stash the notice; got %q", pendingUpgradeNotice)
	}
}

func TestNoteVersionTransition_QuietVerbsSkip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)
	if err := os.WriteFile(p.LastVersionFile(), []byte("v0.14.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"completion", "_complete-verbs", "wingd", "handle-trigger"} {
		var buf bytes.Buffer
		noteVersionTransition(&buf, verb)
		if buf.Len() != 0 {
			t.Errorf("verb %q should be quiet; got %q", verb, buf.String())
		}
	}
	stamp, _ := os.ReadFile(p.LastVersionFile())
	if strings.TrimSpace(string(stamp)) != "v0.14.0" {
		t.Fatalf("quiet verb rewrote the stamp: %q", strings.TrimSpace(string(stamp)))
	}
}

func TestNoteVersionTransition_FirstEverRunSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)

	var buf bytes.Buffer
	noteVersionTransition(&buf, "version")
	if buf.Len() != 0 {
		t.Fatalf("first-ever run (no stamp) should be silent; got %q", buf.String())
	}
	if _, err := os.Stat(p.LastVersionFile()); err != nil {
		t.Fatalf("first run should have written the stamp: %v", err)
	}
	_ = filepath.Base(home)
}
