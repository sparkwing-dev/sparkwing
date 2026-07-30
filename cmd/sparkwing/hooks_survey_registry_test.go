package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// corruptRegistry points the machine at a repos.yaml that will not parse,
// which is the state a half-finished concurrent append leaves behind.
func (f *chainFixture) corruptRegistry(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.root, "repos.yaml")
	writeRepoFile(t, path, "repos:\n  - path: "+f.repo+"\nfallback_paths:\n  - ~/code\ne\n")
	t.Setenv("SPARKWING_REPOS", path)
}

// armedFleet registers one checkout and arms its gate, so the survey has a
// fleet where every declared gate really does fire.
func armedFleet(t *testing.T) {
	t.Helper()
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.registerRepos(t, f.repo)
	captureStdout(t, func() {
		if err := runHooksInstall([]string{"--repo", f.repo}); err != nil {
			t.Fatalf("hooks install: %v", err)
		}
	})
}

// The negative control this whole surface turns on: a registry that cannot be
// read must not produce the output of a fleet whose gates all fire. Both
// answers used to be an empty list, so a repos.yaml with one stray character
// reported a clean machine and every ungated repo on it went unmentioned.
func TestHooksSurvey_UnreadableRegistryReadsNothingLikeAGatedFleet(t *testing.T) {
	armedFleet(t)
	gatedOut := captureStdout(t, func() {
		if err := runHooksSurvey([]string{"-o", "pretty"}); err != nil {
			t.Fatalf("survey of a gated fleet: %v", err)
		}
	})
	if !strings.Contains(gatedOut, "every declared gate fires") {
		t.Fatalf("the armed fixture did not survey as gated, so this test proves nothing:\n%s", gatedOut)
	}

	blind := newChainFixture(t)
	blind.asProcessEnv(t)
	blind.corruptRegistry(t)
	var blindErr error
	blindOut := captureStdout(t, func() {
		blindErr = runHooksSurvey([]string{"-o", "pretty"})
	})

	if blindErr == nil {
		t.Fatal("survey exited zero on a registry it could not read: a caller checking the exit code is told the fleet is fine")
	}
	if blindOut == gatedOut {
		t.Fatalf("a registry that would not parse renders exactly like a fully gated fleet:\n%s", blindOut)
	}
	if strings.Contains(blindOut, "every declared gate fires") {
		t.Errorf("survey claimed every gate fires having read no repos:\n%s", blindOut)
	}
	if strings.Contains(blindOut, "no repos registered") {
		t.Errorf("survey reported an empty registry when the registry was unreadable:\n%s", blindOut)
	}
	if !strings.Contains(blindErr.Error(), "repos.yaml") {
		t.Errorf("the error does not name the file to fix: %v", blindErr)
	}
}

// -o json is the form a script reads, and `[]` there is the machine-readable
// spelling of "every repo is fine". An unreadable registry must not print it.
func TestHooksSurvey_UnreadableRegistryEmitsNoJSONArray(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.corruptRegistry(t)

	var err error
	out := captureStdout(t, func() {
		err = runHooksSurvey([]string{"-o", "json", "--ungated"})
	})
	if err == nil {
		t.Fatal("survey --ungated -o json exited zero on an unreadable registry")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("survey wrote a document a script would parse as an answer:\n%s", out)
	}
}

// The registry is also what install --fleet and fire --fleet enumerate. An
// empty sweep there reports a fleet swept and every gate proven, having
// touched nothing.
func TestFleetSweeps_RefuseAnUnreadableRegistry(t *testing.T) {
	for name, run := range map[string]func() error{
		"install": func() error { return installFleet(installOptions{}) },
		"fire":    func() error { return runHooksFire([]string{"--fleet", "-o", "plain"}) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newChainFixture(t)
			f.asProcessEnv(t)
			f.corruptRegistry(t)

			var err error
			out := captureStdout(t, func() { err = run() })
			if err == nil {
				t.Fatalf("%s --fleet swept a registry it could not read and reported success:\n%s", name, out)
			}
			if strings.Contains(out, "no repos registered") {
				t.Errorf("%s --fleet reported an empty registry when it could not read one:\n%s", name, out)
			}
		})
	}
}

// doctor keeps reporting the rest of the sweep, so the registry failure has to
// travel as its own finding. Without it the ungated list is empty on a machine
// nobody looked at, which is the same field a fully gated fleet leaves empty.
func TestDoctorDiagnose_CarriesTheReasonTheGateSurveyDidNotRun(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.corruptRegistry(t)
	t.Setenv("SPARKWING_HOME", filepath.Join(f.root, "home"))
	if err := os.MkdirAll(filepath.Join(f.root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := diagnose(t.Context(), mustHomePaths(t, ""), "", true)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if report.GatesSurveyError == "" {
		t.Fatal("doctor reported no ungated repos without saying it never read the registry")
	}
	if report.GatesSurveyed != 0 {
		t.Errorf("gates_surveyed = %d, want 0: nothing was surveyed", report.GatesSurveyed)
	}
}

func mustHomePaths(t *testing.T, home string) paths.Paths {
	t.Helper()
	p, err := homePaths(home)
	if err != nil {
		t.Fatalf("homePaths: %v", err)
	}
	return p
}
