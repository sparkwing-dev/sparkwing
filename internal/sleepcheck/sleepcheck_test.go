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

	got, err := scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 1 || got[0].file != "widget_test.go" || got[0].line != 5 {
		t.Fatalf("findings = %v, want only the test file's sleep; product code keeps its timers", got)
	}
}

func TestScan_FailsATestFileItCannotParse(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken_test.go"), []byte("package widget\n\nfunc Broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := scan(root)
	if err == nil {
		t.Fatal("scan passed a file it could not read, so the run reports a verdict it never reached")
	}
	if !strings.Contains(err.Error(), "broken_test.go") {
		t.Errorf("error = %q, does not name the file", err)
	}
}

func TestReport_NamesTheAlternatives(t *testing.T) {
	for _, want := range []string{"synctest", "fake clock", "signaled", "shrinks"} {
		if !strings.Contains(advice+alternatives, want) {
			t.Errorf("the failure advice does not name %q:\n%s\n%s", want, alternatives, advice)
		}
	}
	if strings.Contains(advice, "TODO") {
		t.Errorf("the advice carries a marker the comment gate refuses:\n%s", advice)
	}
}

func TestSplit_FailsNewOffendersAndNamesStaleEntries(t *testing.T) {
	findings := []finding{
		{file: "a_test.go", line: 3, form: "time.Sleep"},
		{file: "b_test.go", line: 9, form: "time.After"},
	}
	baseline := map[string]bool{"a_test.go:3": true, "gone_test.go:12": true}

	fresh, stale := split(findings, baseline)

	if len(fresh) != 1 || fresh[0].file != "b_test.go" {
		t.Fatalf("fresh = %v, want only the offender the baseline does not carry", fresh)
	}
	if len(stale) != 1 || stale[0] != "gone_test.go:12" {
		t.Fatalf("stale = %v, want the entry that no longer offends", stale)
	}
}

func TestBaseline_RoundTripsAndOnlyShrinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	findings := []finding{
		{file: "b_test.go", line: 9},
		{file: "a_test.go", line: 3},
	}
	if err := writeBaseline(path, findings); err != nil {
		t.Fatalf("writeBaseline: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.HasPrefix(body, "#") {
		t.Errorf("the baseline opens without the header that says how to regenerate it:\n%s", body)
	}
	if strings.Index(body, "a_test.go:3") > strings.Index(body, "b_test.go:9") {
		t.Errorf("the baseline is unsorted, so two purges conflict on every line:\n%s", body)
	}

	entries, err := readBaseline(path)
	if err != nil {
		t.Fatalf("readBaseline: %v", err)
	}
	if len(entries) != 2 || !entries["a_test.go:3"] || !entries["b_test.go:9"] {
		t.Fatalf("readBaseline = %v, want both offenders", entries)
	}

	if err := writeBaseline(path, findings[:1]); err != nil {
		t.Fatalf("writeBaseline: %v", err)
	}
	shrunk, err := readBaseline(path)
	if err != nil {
		t.Fatalf("readBaseline: %v", err)
	}
	if len(shrunk) != 1 || !shrunk["b_test.go:9"] {
		t.Fatalf("readBaseline = %v after a purge, want only the surviving offender", shrunk)
	}
}

func TestReadBaseline_TreatsAMissingFileAsEmpty(t *testing.T) {
	entries, err := readBaseline(filepath.Join(t.TempDir(), "absent.txt"))
	if err != nil {
		t.Fatalf("readBaseline: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want an empty baseline", entries)
	}
}

func TestStaleReport_NamesTheRegenerationCommand(t *testing.T) {
	out := staleReport([]string{"a_test.go:3"}, "internal/sleepcheck/baseline.txt")
	for _, want := range []string{"a_test.go:3", "-write-baseline", "only shrinks"} {
		if !strings.Contains(out, want) {
			t.Errorf("the stale report does not say %q:\n%s", want, out)
		}
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

	findings, err := scan(repo)
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
		"-write-baseline",
		"only shrinks",
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
