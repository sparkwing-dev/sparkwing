package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func checkSource(t *testing.T, body string) []finding {
	t.Helper()
	path := filepath.Join(t.TempDir(), "widget_test.go")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := checkFile(path, "widget_test.go")
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	return got
}

func formsByLine(findings []finding) map[int]string {
	byLine := map[int]string{}
	for _, f := range findings {
		byLine[f.line] = f.form
	}
	return byLine
}

func TestCheckFile_RejectsEveryBannedTimerCall(t *testing.T) {
	src := `package widget

import (
	"testing"
	"time"
)

func TestWaits(t *testing.T) {
	time.Sleep(10 * time.Millisecond)
	<-time.After(time.Second)
	<-time.Tick(time.Second)
	timer := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Second)
	_, _ = timer, ticker
}
`
	got := formsByLine(checkSource(t, src))
	want := map[int]string{
		9:  "time.Sleep",
		10: "time.After",
		11: "time.Tick",
		12: "time.NewTimer",
		13: "time.NewTicker",
	}
	for line, form := range want {
		if got[line] != form {
			t.Errorf("line %d = %q, want %q", line, got[line], form)
		}
	}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want exactly the five banned calls", got)
	}
}

func TestCheckFile_AllowsAFixtureStamp(t *testing.T) {
	src := `package widget

import (
	"testing"
	"time"
)

type run struct {
	created time.Time
	label   string
}

func TestStamp(t *testing.T) {
	r := run{created: time.Now(), label: "x"}
	created := time.Now()
	if r.created.IsZero() || created.IsZero() {
		t.Fatal("no stamp")
	}
	if r.label != "x" {
		t.Fatal("no label")
	}
}
`
	if got := checkSource(t, src); len(got) != 0 {
		t.Fatalf("findings = %v; a stamped fixture value is allowed in this phase", got)
	}
}

func TestCheckFile_RejectsTheWallClockAsAWait(t *testing.T) {
	src := `package widget

import (
	"context"
	"testing"
	"time"
)

func TestWallClock(t *testing.T) {
	start := time.Now()
	for time.Since(start) < time.Second {
		_ = start
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("too slow")
	}
	elapsed := time.Since(start)
	if elapsed > time.Minute {
		t.Fatal("too slow")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	_ = ctx
}
`
	got := formsByLine(checkSource(t, src))
	want := map[int]string{
		11: "a loop that waits on the wall clock",
		14: "a deadline compared against the wall clock",
		18: "a deadline compared against the wall clock",
		21: "context.WithDeadline on a wall-clock deadline",
	}
	for line, form := range want {
		if got[line] != form {
			t.Errorf("line %d = %q, want %q", line, got[line], form)
		}
	}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}

func TestCheckFile_RejectsAWallClockContextTimeout(t *testing.T) {
	src := `package widget

import (
	"context"
	"testing"
	"time"
)

func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Until(time.Now().Add(time.Second)))
	defer cancel()
	budget, cancelBudget := context.WithTimeout(context.Background(), time.Second)
	defer cancelBudget()
	_, _ = ctx, budget
}
`
	got := formsByLine(checkSource(t, src))
	if got[10] != "context.WithTimeout on a wall-clock deadline" {
		t.Errorf("line 10 = %q, want the wall-clock timeout rejected", got[10])
	}
	if len(got) != 1 {
		t.Errorf("findings = %v; a fixed timeout budget carries no wall-clock read", got)
	}
}

func TestCheckFile_LeavesAnAliasedTimeImportJudged(t *testing.T) {
	src := `package widget

import (
	clock "time"
	"testing"
)

func TestAliased(t *testing.T) {
	clock.Sleep(clock.Millisecond)
}
`
	got := formsByLine(checkSource(t, src))
	if got[9] != "time.Sleep" {
		t.Fatalf("findings = %v; an aliased time import hides the sleep", got)
	}
}

func TestScan_JudgesOnlyTestFiles(t *testing.T) {
	root := t.TempDir()
	product := "package widget\n\nimport \"time\"\n\nfunc Wait() { time.Sleep(time.Second) }\n"
	if err := os.WriteFile(filepath.Join(root, "widget.go"), []byte(product), 0o644); err != nil {
		t.Fatal(err)
	}
	test := "package widget\n\nimport \"time\"\n\nfunc waitInTest() { time.Sleep(time.Second) }\n"
	if err := os.WriteFile(filepath.Join(root, "widget_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 1 || got[0].file != "widget_test.go" || got[0].line != 5 {
		t.Fatalf("findings = %v, want only the test file's sleep; product code keeps its timers", got)
	}
}

func TestScan_ReportsAFileItCannotParseWithoutFailingTheWalk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken_test.go"), []byte("package widget\n\nfunc Broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, unread, err := scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(unread) != 1 || filepath.Base(unread[0].file) != "broken_test.go" {
		t.Fatalf("unreadable = %+v, want the file the parser rejected", unread)
	}
	if out := unreadableFailure(unread); !strings.Contains(out, "reached no verdict") {
		t.Errorf("failure = %q, does not say the run judged nothing there", out)
	}
}

func TestOnlyChanged_LeavesABrokenFileOutsideTheScopeAlone(t *testing.T) {
	root := t.TempDir()
	unread := []unreadable{
		{file: filepath.Join(root, "touched_test.go"), err: os.ErrInvalid},
		{file: filepath.Join(root, "untouched_test.go"), err: os.ErrInvalid},
	}
	added := map[string]map[int]bool{"touched_test.go": {3: true}}

	got := onlyChanged(unread, root, added)

	if len(got) != 1 || filepath.Base(got[0].file) != "touched_test.go" {
		t.Fatalf("onlyChanged = %+v; a test file this commit never touched blocks it", got)
	}
}

func TestReport_NamesTheAlternatives(t *testing.T) {
	for _, want := range []string{"synctest", "fake clock", "signaled"} {
		if !strings.Contains(advice+alternatives, want) {
			t.Errorf("the failure advice does not name %q:\n%s\n%s", want, alternatives, advice)
		}
	}
	if strings.Contains(advice, "TODO") {
		t.Errorf("the advice carries a marker the comment gate refuses:\n%s", advice)
	}
}

func TestScopedAdds_ChargesTheBranchAndSkipsCommittedLines(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, filepath.Join(repo, "added_test.go"),
		"package p\n\nimport \"time\"\n\nfunc wait() { time.Sleep(time.Second) }\n")

	added, err := scopedAdds(repo, false, "main")
	if err != nil {
		t.Fatalf("scopedAdds: %v", err)
	}
	if !added["added_test.go"][5] {
		t.Errorf("added = %v; a sleep in an untracked test file is not charged to the branch", added)
	}
	if _, ok := added["base_test.go"]; ok {
		t.Errorf("added = %v; a line the base already carried is charged to the branch", added)
	}

	findings, _, err := scan(repo)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	scoped := onlyAdded(findings, added)
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want the base sleep and the new one", findings)
	}
	if len(scoped) != 1 || scoped[0].file != "added_test.go" {
		t.Fatalf("scoped = %v, want only the sleep this branch added", scoped)
	}
}

func TestScopedAdds_FailsWhenTheBaseCannotBeResolved(t *testing.T) {
	if _, err := scopedAdds(t.TempDir(), false, "origin/main"); err == nil {
		t.Fatal("scopedAdds reported a diff outside a repository, so the gate would pass ungated")
	}
}

func TestDiffFailure_NamesTheFixAndTheEscape(t *testing.T) {
	msg := diffFailure("origin/main", os.ErrNotExist)
	for _, want := range []string{"origin/main", "nothing was gated", "git fetch origin main", "-allow-no-diff"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the diff failure does not name %q: %s", want, msg)
		}
	}
	if got := diffFailure("", os.ErrNotExist); !strings.Contains(got, "the staged diff") {
		t.Errorf("the staged-mode failure does not name its scope: %s", got)
	}
}

func TestUsage_NamesTheBannedFormsAndTheBaseline(t *testing.T) {
	for _, want := range []string{
		"<root>",
		"time.Sleep",
		"time.NewTicker",
		"stamps a fixture value",
		"dot-imports time",
		"-base",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("the usage text does not name %q:\n%s", want, usageText)
		}
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "base_test.go"),
		"package p\n\nimport \"time\"\n\nfunc baseWait() { time.Sleep(time.Second) }\n")
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "add", "base_test.go")
	runGit(t, dir, "commit", "-m", "base")
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=sleepcheck test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=sleepcheck test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCheckFile_RejectsTimeUntilAsAWait(t *testing.T) {
	src := `package widget

import (
	"testing"
	"time"
)

func TestUntil(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	for time.Until(deadline) > 0 {
		_ = deadline
	}
	remaining := time.Until(deadline)
	if remaining > time.Millisecond {
		t.Fatal("too slow")
	}
}
`
	got := formsByLine(checkSource(t, src))
	want := map[int]string{
		10: "a loop that waits on the wall clock",
		14: "a deadline compared against the wall clock",
	}
	for line, form := range want {
		if got[line] != form {
			t.Errorf("line %d = %q, want %q", line, got[line], form)
		}
	}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}

func TestCheckFile_RejectsATimerTakenAsAValue(t *testing.T) {
	src := `package widget

import (
	"testing"
	"time"
)

func TestValue(t *testing.T) {
	nap := time.Sleep
	nap(time.Millisecond)
}
`
	got := formsByLine(checkSource(t, src))
	if got[9] != "time.Sleep taken as a value" {
		t.Fatalf("findings = %v; a banned timer handed to a variable is called out of this walk's sight", got)
	}
}

func TestCheckFile_RefusesAFileThatDotImportsTime(t *testing.T) {
	src := `package widget

import (
	"testing"
	. "time"
)

func TestDotted(t *testing.T) {
	Sleep(Millisecond)
}
`
	got := checkSource(t, src)
	if len(got) != 1 || got[0].form != unjudgeable {
		t.Fatalf("findings = %v, want the file refused; an unqualified Sleep is invisible to this walk", got)
	}
}

func TestCheckFile_AllowsOnlySynctestBodies(t *testing.T) {
	for _, alias := range []string{"synctest", "fake"} {
		src := `package widget
import (
 "testing"
 "time"
 ` + alias + ` "testing/synctest"
)
func TestWait(t *testing.T) {
 time.Sleep(time.Second)
 ` + alias + `.Test(func() *testing.T { time.Sleep(time.Second); return t }(), func(t *testing.T) {
  time.Sleep(time.Hour)
  if time.Since(time.Now()) < time.Second { <-time.After(time.Second) }
 })
 time.Sleep(time.Second)
}
`
		got := checkSource(t, src)
		if len(got) != 3 {
			t.Fatalf("alias %s: findings = %v, want the three waits outside the fake clock", alias, got)
		}
		for _, finding := range got {
			if finding.line != 8 && finding.line != 9 && finding.line != 13 {
				t.Errorf("unexpected finding: %v", finding)
			}
		}
	}
}

func TestCheckFile_RejectsShadowedSynctest(t *testing.T) {
	src := `package widget
import (
 "testing"
 "time"
 "testing/synctest"
)
func TestWait(t *testing.T) {
 synctest.Test(t, func(t *testing.T) { time.Sleep(time.Hour) })
 synctest := struct { Test func(*testing.T, func(*testing.T)) }{}
 synctest.Test(t, func(t *testing.T) { time.Sleep(time.Second) })
}
`
	got := checkSource(t, src)
	if len(got) != 1 || got[0].line != 10 {
		t.Fatalf("findings = %v, want the shadowed Test callback's wait", got)
	}
}
