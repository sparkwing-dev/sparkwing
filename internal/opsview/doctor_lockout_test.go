package opsview

import (
	"os"
	"path/filepath"
	"testing"
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

func repoPinned(t *testing.T, base, name, pin string) string {
	t.Helper()
	sw := filepath.Join(base, name, ".sparkwing")
	if err := os.MkdirAll(sw, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "module example.com/" + name + "\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing " + pin + "\n"
	if err := os.WriteFile(filepath.Join(sw, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, name)
}

func TestDiagnoseLockedOutRepos_NamesReposPinnedBelowTheDaemonsProtocol(t *testing.T) {
	base := t.TempDir()
	behind := repoPinned(t, base, "workwing", "v0.17.25")
	current := repoPinned(t, base, "bitwing", "v0.22.0")
	registerRepos(t, behind, current)

	var report DoctorReport
	diagnoseLockedOutRepos("v0.22.0", &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("want exactly the behind repo reported; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Name != "workwing" || got.Pin != "v0.17.25" {
		t.Errorf("locked-out row = %+v, want workwing at v0.17.25", got)
	}
	if report.Clean() {
		t.Error("a report naming locked-out repos must not be clean")
	}
}

// TestDiagnoseLockedOutRepos_SkipsAReposWhoseWorkspaceOverridesThePin is the
// regression test for the false accusation this check would otherwise make:
// four real checkouts carry a .sparkwing/go.work using the local sparkwing, so
// their declared pin is inert and reporting it would send an operator to edit
// a line that changes nothing.
func TestDiagnoseLockedOutRepos_SkipsAReposWhoseWorkspaceOverridesThePin(t *testing.T) {
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
	diagnoseLockedOutRepos("v0.22.0", &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("a workspace-overridden pin must not be reported; got %+v", report.LockedOutRepos)
	}
}

// TestDiagnoseLockedOutRepos_SilentWhenTheDaemonSpeaksTheOlderProtocol keeps
// the check from firing in the direction that works: an older daemon serves
// every older client, and a newer client takes it over.
func TestDiagnoseLockedOutRepos_SilentWhenTheDaemonSpeaksTheOlderProtocol(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos("v0.20.0", &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("no repo is locked out by a protocol-1 daemon; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_SilentWithoutAReadableDaemonVersion(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	for _, v := range []string{"", "(unknown)"} {
		var report DoctorReport
		diagnoseLockedOutRepos(v, &report)
		if len(report.LockedOutRepos) != 0 {
			t.Errorf("daemon version %q is not evidence; got %+v", v, report.LockedOutRepos)
		}
	}
}
