package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// noticeHome points the process at a throwaway sparkwing home and
// returns its paths plus the running test binary's own stamp file.
func noticeHome(t *testing.T) (paths.Paths, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	pendingUpgradeNotice = ""
	p := paths.PathsAt(home)
	self, err := installsite.Self()
	if err != nil {
		t.Fatal(err)
	}
	return p, p.VersionStampFile(installsite.PathKey(self))
}

// seedStamp writes a stamp file as if the install at exe last ran as
// version, the way writeVersionStamp lays it down.
func seedStamp(t *testing.T, file, exe, version string) {
	t.Helper()
	writeVersionStamp(file, exe, version)
	if got := readVersionStamp(file); got != version {
		t.Fatalf("seed stamp round-trip = %q, want %q", got, version)
	}
}

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
	_, stamp := noticeHome(t)
	seedStamp(t, stamp, "test-binary", "v0.14.0")

	var first bytes.Buffer
	noteVersionTransition(&first, "version")
	if !strings.Contains(first.String(), "sparkwing changed v0.14.0 -> ") {
		t.Fatalf("first run did not emit the transition line; got %q", first.String())
	}
	if !strings.Contains(first.String(), "--topic changelog") {
		t.Fatalf("transition line missing the changelog pointer; got %q", first.String())
	}

	if got := readVersionStamp(stamp); got != installedVersion() {
		t.Fatalf("stamp = %q, want %q", got, installedVersion())
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
	_, stamp := noticeHome(t)
	seedStamp(t, stamp, "test-binary", "v0.14.0")

	var buf bytes.Buffer
	noteVersionTransition(&buf, "info")
	if buf.Len() != 0 {
		t.Fatalf("info verb should not write the line to the stream; got %q", buf.String())
	}
	if !strings.Contains(pendingUpgradeNotice, "sparkwing changed v0.14.0 -> ") {
		t.Fatalf("info verb did not stash the notice; got %q", pendingUpgradeNotice)
	}
}

func TestUpgradeNoticeLineDoesNotClaimVersionOrder(t *testing.T) {
	tests := []struct {
		previous string
		current  string
	}{
		{"v0.22.2-0.20260801181107-3e089db19798", "v0.22.2-0.20260801163726-c86d57474ce4"},
		{"(devel)", "v0.22.2-0.20260801163726-c86d57474ce4"},
		{"v0.22.2-0.20260801181107-3e089db19798", "(devel)"},
	}
	for _, test := range tests {
		line := upgradeNoticeLine(test.previous, test.current)
		if strings.Contains(line, "upgraded") {
			t.Fatalf("transition %q -> %q claimed an upgrade: %q", test.previous, test.current, line)
		}
		if !strings.Contains(line, "sparkwing changed "+test.previous+" -> "+test.current) {
			t.Fatalf("transition line = %q", line)
		}
	}
}

func TestNoteVersionTransition_QuietVerbsSkip(t *testing.T) {
	_, stamp := noticeHome(t)
	seedStamp(t, stamp, "test-binary", "v0.14.0")

	for _, verb := range []string{"completion", "_complete-verbs", "wingd", "handle-trigger"} {
		var buf bytes.Buffer
		noteVersionTransition(&buf, verb)
		if buf.Len() != 0 {
			t.Errorf("verb %q should be quiet; got %q", verb, buf.String())
		}
	}
	if got := readVersionStamp(stamp); got != "v0.14.0" {
		t.Fatalf("quiet verb rewrote the stamp: %q", got)
	}
}

func TestNoteVersionTransition_FirstEverRunSilent(t *testing.T) {
	_, stamp := noticeHome(t)

	var buf bytes.Buffer
	noteVersionTransition(&buf, "version")
	if buf.Len() != 0 {
		t.Fatalf("first-ever run (no stamp) should be silent; got %q", buf.String())
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("first run should have written this install's stamp: %v", err)
	}
}
