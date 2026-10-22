package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *chainFixture) corruptRegistry(t *testing.T) {
	t.Helper()
	writeRegistry(t, filepath.Join(f.root, "config.yaml"), "repos:\n  - path: "+f.repo+"\nfallback_paths:\n  - ~/code\ne\n")
}

func armedFleet(t *testing.T) {
	t.Helper()
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.registerRepos(t, f.repo)
	captureStdout(t, func() {
		if err := runIn(t, f.repo, parseHooksInstall, "--no-prove"); err != nil {
			t.Fatalf("hooks install: %v", err)
		}
	})
}

func TestHooksSurvey_UnreadableRegistryReadsNothingLikeAGatedFleet(t *testing.T) {
	armedFleet(t)
	gatedOut := captureStdout(t, func() {
		if err := parseHooksStatus([]string{"--all-repos", "-o", "pretty"}); err != nil {
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
		blindErr = parseHooksStatus([]string{"--all-repos", "-o", "pretty"})
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
	if !strings.Contains(blindErr.Error(), "config.yaml") {
		t.Errorf("the error does not name the file to fix: %v", blindErr)
	}
}

func TestHooksSurvey_UnreadableRegistryEmitsNoJSONRecords(t *testing.T) {
	f := newChainFixture(t)
	f.asProcessEnv(t)
	f.corruptRegistry(t)

	var err error
	out := captureStdout(t, func() {
		err = parseHooksStatus([]string{"--all-repos", "-o", "json", "--ungated"})
	})
	if err == nil {
		t.Fatal("survey --ungated -o json exited zero on an unreadable registry")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("survey wrote a document a script would parse as an answer:\n%s", out)
	}
}

func TestFleetSweeps_RefuseAnUnreadableRegistry(t *testing.T) {
	for name, run := range map[string]func() error{
		"install": func() error { return installFleet(installOptions{}) },
		"status":  func() error { return parseHooksStatus([]string{"--all-repos", "-o", "plain"}) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newChainFixture(t)
			f.asProcessEnv(t)
			f.corruptRegistry(t)

			var err error
			out := captureStdout(t, func() { err = run() })
			if err == nil {
				t.Fatalf("%s --all-repos swept a registry it could not read and reported success:\n%s", name, out)
			}
			if strings.Contains(out, "no repos registered") {
				t.Errorf("%s --all-repos reported an empty registry when it could not read one:\n%s", name, out)
			}
		})
	}
}

func TestDoctorDiagnose_CarriesTheReasonTheGateSurveyDidNotRun(t *testing.T) {
	f := newChainFixture(t)
	writeExec(t, filepath.Join(f.binDir, "go"), "#!/bin/sh\nprintf 'auto\\ngo1.26.9\\noff\\n'\n")
	f.asProcessEnv(t)
	f.corruptRegistry(t)
	t.Setenv("SPARKWING_HOME", filepath.Join(f.root, "home"))
	if err := os.MkdirAll(filepath.Join(f.root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}

	report := inspectDoctorMachine(t.Context())
	if report.GatesSurveyError == "" {
		t.Fatal("doctor reported no ungated repos without saying it never read the registry")
	}
	if report.GatesSurveyed != 0 {
		t.Errorf("gates_surveyed = %d, want 0: nothing was surveyed", report.GatesSurveyed)
	}
}
