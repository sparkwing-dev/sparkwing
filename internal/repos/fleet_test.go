package repos

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeGit maps a checkout path to the git-common-dir output that
// canonicalizes it, so worktree folding can be tested without real
// linked worktrees.
func fakeGit(commonDirs map[string]string) Git {
	return func(dir string, args ...string) (string, error) {
		if cd, ok := commonDirs[dir]; ok {
			return cd + "\n", nil
		}
		return dir + "/.git\n", nil
	}
}

func writeSparkwingPin(t *testing.T, root, pin string) {
	t.Helper()
	sw := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(sw, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "module example.com/pipes\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing " + pin + "\n"
	if err := os.WriteFile(filepath.Join(sw, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveFleet_FoldsWorktreeIntoPrimary(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "app")
	worktree := filepath.Join(base, "app-feature")
	writeSparkwingPin(t, primary, "v0.15.6")
	writeSparkwingPin(t, worktree, "v0.15.8")

	git := fakeGit(map[string]string{
		primary:  filepath.Join(primary, ".git"),
		worktree: filepath.Join(primary, ".git"),
	})
	cands := []Candidate{
		{Path: primary},
		{Path: worktree, Worktree: true},
	}
	fleet := DeriveFleet(cands, nil, git, "", nil)
	if len(fleet) != 1 {
		t.Fatalf("worktree should fold into primary; got %d rows: %+v", len(fleet), fleet)
	}
	r := fleet[0]
	if r.Pin != "v0.15.6" {
		t.Errorf("primary pin = %q, want v0.15.6 (worktree pin must not win)", r.Pin)
	}
	div := r.DivergentWorktrees()
	if len(div) != 1 || div[0].Pin != "v0.15.8" {
		t.Errorf("divergent worktree not reported: %+v", div)
	}
}

func TestDeriveFleet_RunsOnlyRepoSurfaced(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "app")
	writeSparkwingPin(t, primary, "v0.15.6")
	git := fakeGit(map[string]string{primary: filepath.Join(primary, ".git")})

	runs := []RunObservation{
		{Repo: "app", Pipeline: "build", At: time.Unix(1000, 0)},
		{Repo: "other-service", Pipeline: "deploy", At: time.Unix(2000, 0)},
	}
	fleet := DeriveFleet([]Candidate{{Path: primary}}, runs, git, "", nil)
	if len(fleet) != 2 {
		t.Fatalf("want 2 rows (checkout + runs-only), got %d", len(fleet))
	}
	var appRow, runsOnly *Repo
	for i := range fleet {
		switch fleet[i].Name {
		case "app":
			appRow = &fleet[i]
		case "other-service":
			runsOnly = &fleet[i]
		}
	}
	if appRow == nil || appRow.LastPipeline != "build" {
		t.Errorf("app row should carry last run 'build': %+v", appRow)
	}
	if runsOnly == nil || runsOnly.Status != "runs-only" {
		t.Errorf("other-service should be runs-only: %+v", runsOnly)
	}
}

func TestGuidesBehind_CountsCrossedRange(t *testing.T) {
	guides := []string{"v0.15.5", "v0.15.7", "v0.15.8", "v0.16.0"}
	if got := GuidesBehind(guides, "v0.15.6", "v0.15.8"); got != 2 {
		t.Errorf("GuidesBehind = %d, want 2 (v0.15.7, v0.15.8)", got)
	}
	if got := GuidesBehind(guides, "v0.15.8", "v0.15.8"); got != 0 {
		t.Errorf("GuidesBehind at target = %d, want 0", got)
	}
	if got := GuidesBehind(guides, "notsemver", "v0.16.0"); got != 0 {
		t.Errorf("GuidesBehind with bad pin = %d, want 0", got)
	}
}

func TestPinsDiverge(t *testing.T) {
	same := []Repo{{Primary: "/a", Pin: "v1"}, {Primary: "/b", Pin: "v1"}}
	if PinsDiverge(same) {
		t.Error("matching pins should not diverge")
	}
	diff := []Repo{{Primary: "/a", Pin: "v1"}, {Primary: "/b", Pin: "v2"}}
	if !PinsDiverge(diff) {
		t.Error("mismatched pins should diverge")
	}
}
