package opsview

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

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

func shippedFloors() wingwire.ProtocolFloors { return wingwire.ReleasedProtocolFloors() }

func floorsBeforeProtocol3() wingwire.ProtocolFloors {
	return wingwire.ProtocolFloors{
		{Major: 1, MinVersion: "v0.0.0"},
		{Major: 2, MinVersion: "v0.22.0"},
	}
}

func renderPretty(t *testing.T, r DoctorReport) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestDiagnoseLockedOutRepos_NamesReposPinnedBelowTheDaemonsProtocol(t *testing.T) {
	base := t.TempDir()
	behind := repoPinned(t, base, "workwing", "v0.17.25")
	current := repoPinned(t, base, "bitwing", "v0.22.0")
	registerRepos(t, behind, current)

	var report DoctorReport
	diagnoseLockedOutRepos(2, "v0.22.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("want exactly the behind repo reported; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Name != "workwing" || got.Pin != "v0.17.25" || got.RaiseTo != "v0.22.0" {
		t.Errorf("locked-out row = %+v, want workwing pinned v0.17.25 raising to v0.22.0", got)
	}
	if report.DaemonProtocolGap != nil {
		t.Errorf("the table covers protocol 2; got a gap %+v", report.DaemonProtocolGap)
	}
	if report.Clean() {
		t.Error("a report naming locked-out repos must not be clean")
	}
}

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
	diagnoseLockedOutRepos(2, "v0.22.0", afterNextBump, &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("a protocol-2 daemon locks out every protocol-1 pin; got %+v", report.LockedOutRepos)
	}
	got := report.LockedOutRepos[0]
	if got.Name != "workwing" || got.RaiseTo != "v0.22.0" {
		t.Errorf("locked-out row = %+v, want workwing raising to v0.22.0, not to the newest floor", got)
	}
}

func TestDiagnoseLockedOutRepos_RaisesToTheDaemonsOwnReleaseWhenTheTableEndsBelowItsProtocol(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos(3, "v0.30.0", floorsBeforeProtocol3(), &report)

	if len(report.LockedOutRepos) != 1 {
		t.Fatalf("a protocol-3 daemon locks out a protocol-1 pin; got %+v", report.LockedOutRepos)
	}
	if got := report.LockedOutRepos[0].RaiseTo; got != "v0.30.0" {
		t.Errorf("row raises to %q; v0.22.0 speaks protocol 2 and stays refused by a protocol-3 daemon", got)
	}
}

func TestDiagnoseLockedOutRepos_NamesPinsAtTheNewestKnownMajorWhenTheDaemonIsPastIt(t *testing.T) {
	base := t.TempDir()
	registerRepos(t,
		repoPinned(t, base, "workwing", "v0.17.25"),
		repoPinned(t, base, "bitwing", "v0.23.0"),
		repoPinned(t, base, "dowing", "v0.30.0"),
	)

	var report DoctorReport
	diagnoseLockedOutRepos(3, "v0.30.0", floorsBeforeProtocol3(), &report)

	got := map[string]string{}
	for _, row := range report.LockedOutRepos {
		got[row.Name] = row.RaiseTo
	}
	want := map[string]string{"workwing": "v0.30.0", "bitwing": "v0.30.0"}
	if len(got) != len(want) {
		t.Fatalf("locked-out rows = %+v, want workwing and bitwing -- dowing already carries the daemon's release", report.LockedOutRepos)
	}
	for name, raiseTo := range want {
		if got[name] != raiseTo {
			t.Errorf("row %s raises to %q, want %q", name, got[name], raiseTo)
		}
	}
}

func TestDiagnoseLockedOutRepos_ReportsADaemonSpeakingAMajorTheTableDoesNotCarry(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos(3, "v0.30.0", floorsBeforeProtocol3(), &report)

	gap := report.DaemonProtocolGap
	if gap == nil {
		t.Fatal("a daemon at protocol 3 against a table ending at 2 is a gap this build must name")
	}
	if gap.Self != 2 || gap.Daemon != 3 || gap.DaemonVersion != "v0.30.0" {
		t.Errorf("gap = %+v, want protocol-2 table against daemon 3 (v0.30.0)", gap)
	}
	if report.Clean() {
		t.Error("a build that cannot speak to the resident daemon is not a clean report")
	}
}

func TestDiagnoseLockedOutRepos_NamesNoTargetWhenANewerDaemonReportsNoRelease(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	for _, version := range []string{"", "(unknown)", scratchModuleVersion, "v0.22.0-dev+b9ade496"} {
		var report DoctorReport
		diagnoseLockedOutRepos(3, version, floorsBeforeProtocol3(), &report)

		if len(report.LockedOutRepos) != 0 {
			t.Errorf("daemon version %q names no release to raise to; got %+v", version, report.LockedOutRepos)
		}
		if report.DaemonProtocolGap == nil {
			t.Errorf("daemon version %q still speaks a protocol this build does not", version)
		}
	}
}

func TestDiagnoseLockedOutRepos_GivesARegisteredWorktreeItsOwnRow(t *testing.T) {
	base := t.TempDir()
	primary := pinnedCheckout(t, base, "sparks-core", "v0.22.0", false)
	worktree := pinnedCheckout(t, base, "feature-worktree", "v0.17.25", true)
	registerRepos(t, primary, worktree)

	var report DoctorReport
	diagnoseLockedOutRepos(2, "v0.22.0", shippedFloors(), &report)

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
	if got.Name != "feature-worktree" || got.Pin != "v0.17.25" {
		t.Errorf("locked-out row = %+v, want feature-worktree pinned v0.17.25", got)
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
	diagnoseLockedOutRepos(2, "v0.22.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("a workspace-overridden pin must not be reported; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_SilentWhenTheDaemonSpeaksTheOlderProtocol(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos(1, "v0.20.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 0 {
		t.Fatalf("no repo is locked out by a protocol-1 daemon; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_NamesReposAgainstADaemonThatReportsNoVersion(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos(2, "", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 1 || report.LockedOutRepos[0].RaiseTo != "v0.22.0" {
		t.Fatalf("the table names protocol 2's floor without help from the daemon's version; got %+v", report.LockedOutRepos)
	}
}

func TestDiagnoseLockedOutRepos_SilentWithoutAReadableProtocolMajor(t *testing.T) {
	base := t.TempDir()
	registerRepos(t, repoPinned(t, base, "workwing", "v0.17.25"))

	var report DoctorReport
	diagnoseLockedOutRepos(0, "v0.30.0", shippedFloors(), &report)

	if len(report.LockedOutRepos) != 0 || report.DaemonProtocolGap != nil {
		t.Fatalf("a daemon that named no protocol major is not evidence; got %+v", report)
	}
}

func TestRenderDoctorPretty_GivesEveryLockedOutRowItsOwnRaiseTarget(t *testing.T) {
	out := renderPretty(t, DoctorReport{LockedOutRepos: []DoctorLockedOutRepo{
		{Name: "workwing", Path: "/code/workwing", Pin: "v0.17.25", RaiseTo: "v0.22.0"},
		{Name: "feature-worktree", Path: "/code/feature-worktree", Pin: "v0.15.4", RaiseTo: "v0.22.0", Worktree: true},
	}})

	for _, want := range []string{
		"workwing", "pinned v0.17.25", "/code/workwing",
		"feature-worktree (worktree)", "pinned v0.15.4", "/code/feature-worktree",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output does not mention %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "raise to v0.22.0"); got != 2 {
		t.Errorf("raise target named %d times, want once per row:\n%s", got, out)
	}
}

func TestRenderDoctorPretty_SaysUpgradingTheCLIDoesNotHelpWhenItKnowsTheDaemonsProtocol(t *testing.T) {
	out := renderPretty(t, DoctorReport{LockedOutRepos: []DoctorLockedOutRepo{
		{Name: "workwing", Path: "/code/workwing", Pin: "v0.17.25", RaiseTo: "v0.22.0"},
	}})

	if !strings.Contains(out, "the sparkwing CLI is not the client here and upgrading it does not help") {
		t.Errorf("pretty output stopped naming the CLI as a bystander:\n%s", out)
	}
	if strings.Contains(out, "update the sparkwing CLI") {
		t.Errorf("nothing here is fixed by updating the CLI:\n%s", out)
	}
}

func TestRenderDoctorPretty_PointsAtUpdatingTheCLIWhenTheDaemonSpeaksPastItsTable(t *testing.T) {
	out := renderPretty(t, DoctorReport{
		LockedOutRepos: []DoctorLockedOutRepo{
			{Name: "workwing", Path: "/code/workwing", Pin: "v0.17.25", RaiseTo: "v0.30.0"},
		},
		DaemonProtocolGap: &DoctorProtocolGap{Self: 2, Daemon: 3, DaemonVersion: "v0.30.0"},
	})

	if strings.Contains(out, "upgrading it does not help") {
		t.Errorf("the CLI is what has to move here; the report argues against it:\n%s", out)
	}
	for _, want := range []string{
		"update the sparkwing CLI",
		"speaks wire protocol 3 and this sparkwing speaks 2",
		"cannot name the lowest release that would do",
		"raise to v0.30.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output does not mention %q:\n%s", want, out)
		}
	}
}

func TestRenderDoctorPretty_ReportsTheProtocolGapWithNoLockedOutCheckouts(t *testing.T) {
	out := renderPretty(t, DoctorReport{
		DaemonProtocolGap: &DoctorProtocolGap{Self: 2, Daemon: 3, DaemonVersion: "v0.30.0"},
	})

	if strings.Contains(out, "healthy") {
		t.Errorf("a daemon this build cannot speak to is not a healthy home:\n%s", out)
	}
	for _, want := range []string{"(sparkwing v0.30.0)", "update the sparkwing CLI"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output does not mention %q:\n%s", want, out)
		}
	}
}
