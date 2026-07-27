package opsview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// registerRepos writes a repos.yaml listing each path and points the loader
// at it, so the lockout scan reads a fixture instead of the real machine.
func registerRepos(t *testing.T, paths ...string) {
	t.Helper()
	cfg := "repos:\n"
	for _, p := range paths {
		cfg += "  - path: " + p + "\n"
	}
	f := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(f, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_REPOS", f)
}

// pinnedCheckout lays down a checkout with an SDK pin. A regular checkout
// carries a .git directory and a linked worktree a .git file, which is what
// the candidate scan reads to tag one.
func pinnedCheckout(t *testing.T, base, name, pin string, worktree bool) string {
	t.Helper()
	root := filepath.Join(base, name)
	sw := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(sw, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "module example.com/" + name + "\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing " + pin + "\n"
	if err := os.WriteFile(filepath.Join(sw, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if worktree {
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+base+"/primary/.git/worktrees/"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func repoPinned(t *testing.T, base, name, pin string) string {
	t.Helper()
	return pinnedCheckout(t, base, name, pin, false)
}

// shippedFloors is the table the binary carries; tests that need a cliff
// this build has not reached yet declare their own.
func shippedFloors() wingwire.ProtocolFloors { return wingwire.ReleasedProtocolFloors() }

func TestDiagnoseLockedOutRepos_NamesReposPinnedBelowTheDaemonsProtocol(t *testing.T) {
	base := t.TempDir()
	behind := repoPinned(t, base, "workwing", "v0.17.25")
	current := repoPinned(t, base, "bitwing", "v0.22.0")
	registerRepos(t, behind, current)

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("want exactly the behind repo reported; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Name != "workwing" || got.Pin != "v0.17.25" || got.RaiseTo != "v0.22.0" {
		t.Errorf("locked-out row = %+v, want workwing pinned v0.17.25 raising to v0.22.0", got)
	}
	if report.Clean() {
		t.Error("a report naming locked-out repos must not be clean")
	}
}

// The daemon's major is compared against each pin's, not against the major
// the scanning binary was built at, so a cliff older than the newest one is
// still diagnosed: after a bump, a daemon left resident at the previous major
// keeps locking out every pin below it.
func TestDiagnoseLockedOutRepos_ReportsACliffOlderThanTheNewestOne(t *testing.T) {
	afterNextBump := wingwire.ProtocolFloors{
		{Major: 1, MinVersion: "v0.0.0"},
		{Major: 2, MinVersion: "v0.22.0"},
		{Major: 3, MinVersion: "v0.30.0"},
	}
	base := t.TempDir()
	behind := repoPinned(t, base, "workwing", "v0.17.25")
	served := repoPinned(t, base, "bitwing", "v0.23.0")
	registerRepos(t, behind, served)

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", afterNextBump, &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("a protocol-2 daemon locks out every protocol-1 pin; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Name != "workwing" || got.RaiseTo != "v0.22.0" {
		t.Errorf("locked-out row = %+v, want workwing raising to v0.22.0, not to the newest floor", got)
	}
}

// A daemon past the newest floor speaks a major the table cannot name, so the
// remedy falls back to the one release known to speak it: the daemon's own.
func TestDiagnoseLockedOutRepos_RaisesToTheDaemonsVersionWhenTheTableEndsBelowIt(t *testing.T) {
	shortTable := wingwire.ProtocolFloors{{Major: 1, MinVersion: "v0.0.0"}}
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", shortTable, &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("a table with one major cannot place two builds on opposite sides; got %+v", report.LockedOutRepos)
	}

	twoOfThree := wingwire.ProtocolFloors{
		{Major: 1, MinVersion: "v0.0.0"},
		{Major: 2, MinVersion: "v0.22.0"},
	}
	report = DoctorReport{}
	diagnoseLockedOutRepos("v0.40.0", twoOfThree, &report)
	if len(report.LockedOutRepos) != 1 || report.LockedOutRepos[0].RaiseTo != "v0.22.0" {
		t.Fatalf("a daemon past the newest floor still speaks that floor's major; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_GivesARegisteredWorktreeItsOwnRow(t *testing.T) {
	base := t.TempDir()
	primary := pinnedCheckout(t, base, "sparks-core", "v0.22.0", false)
	worktree := pinnedCheckout(t, base, "bw-1200", "v0.17.25", true)
	registerRepos(t, primary, worktree)

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("the worktree carries its own refused pin; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Path != worktree {
		t.Errorf("row Path = %q, want the worktree %q -- the primary's pin is not what its gates build", got.Path, worktree)
	}
	if !got.Worktree {
		t.Error("a row for a linked worktree must be marked as one, so its branch-shaped Name reads correctly")
	}
	if got.Name != "bw-1200" || got.Pin != "v0.17.25" {
		t.Errorf("locked-out row = %+v, want bw-1200 pinned v0.17.25", got)
	}
}

func TestDiagnoseLockedOutRepos_SkipsARepoWhoseWorkspaceOverridesThePin(t *testing.T) {
	base := t.TempDir()
	repo := repoPinned(t, base, "sparks-core", "v0.15.4")
	sdk := filepath.Join(base, "sparkwing")
	if err := os.MkdirAll(sdk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdk, "go.mod"),
		[]byte("module github.com/sparkwing-dev/sparkwing\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := "go 1.26\n\nuse (\n\t.\n\t../../sparkwing\n)\n"
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "go.work"), []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	registerRepos(t, repo)

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("a workspace-overridden pin must not be reported; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_SilentWhenTheDaemonSpeaksTheOlderProtocol(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos("v0.20.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("no repo is locked out by a protocol-1 daemon; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_SilentWithoutAReadableDaemonVersion(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	for _, v := range []string{"", "(unknown)"} {
		var report DoctorReport
		diagnoseLockedOutRepos(v, shippedFloors(), &report)
		if len(report.LockedOutRepos) != 0 {
			t.Errorf("daemon version %q is not evidence; got %+v", v, report.LockedOutRepos)
		}
	}
}
