package main

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
)

func TestDoctorCommandRetainsToolchainFinding(t *testing.T) {
	finding := &opsview.DoctorGoToolchain{
		Current: "go1.26.6", Setting: "local", Source: "/path/to/go/env", Floor: "1.26.8",
		Module: "/project/.sparkwing/go.mod", Override: "go1.26.9",
		Verdict: "blocked by GOTOOLCHAIN=local", Error: "Install Go 1.26.8 or newer, or run 'go env -u GOTOOLCHAIN'.",
	}
	for _, format := range []string{"pretty", "plain", "json"} {
		t.Run(format, func(t *testing.T) {
			deps := doctorTestDependencies(&doctorLifecycleFake{})
			deps.machine = func(context.Context) doctorReport { return doctorReport{GoToolchain: finding} }
			var out bytes.Buffer
			if err := runDoctorCommandArgs(t.Context(), []string{"-o", format}, &out, deps); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var got doctorReport
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.GoToolchain == nil || *got.GoToolchain != *finding {
					t.Fatalf("toolchain = %+v", got.GoToolchain)
				}
				return
			}
			for _, want := range []string{finding.Current, finding.Setting, finding.Source, finding.Floor, finding.Module, finding.Override, finding.Verdict, finding.Error} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
		})
	}
}

func TestDoctorCommandNoProjectHasNoToolchainFinding(t *testing.T) {
	for _, format := range []string{"pretty", "plain", "json"} {
		var out bytes.Buffer
		if err := runDoctorCommandArgs(t.Context(), []string{"-o", format}, &out, doctorTestDependencies(&doctorLifecycleFake{})); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "go_toolchain") {
			t.Fatalf("%s invented a toolchain finding: %s", format, out.String())
		}
	}
}

func TestDoctorCommandRetainsInstallConflict(t *testing.T) {
	conflict := &opsview.DoctorInstallConflict{
		Self: "/home/u/.local/bin/sparkwing",
		Competing: []opsview.DoctorInstallCopy{{
			Path: "/home/u/My Apps/sparkwing", Resolved: "/opt/sparkwing/bin/sparkwing", Modified: "2026-08-02T00:50:00Z",
		}},
	}
	for _, format := range []string{"pretty", "plain", "json"} {
		t.Run(format, func(t *testing.T) {
			deps := doctorTestDependencies(&doctorLifecycleFake{})
			deps.machine = func(context.Context) doctorReport { return doctorReport{InstallConflict: conflict} }
			var out bytes.Buffer
			if err := runDoctorCommandArgs(t.Context(), []string{"-o", format}, &out, deps); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var got doctorReport
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.InstallConflict == nil || got.InstallConflict.Self != conflict.Self || len(got.InstallConflict.Competing) != 1 || got.InstallConflict.Competing[0] != conflict.Competing[0] {
					t.Fatalf("install conflict = %+v", got.InstallConflict)
				}
				return
			}
			copy := conflict.Competing[0]
			for _, want := range []string{conflict.Self, copy.Path, copy.Modified, copy.Resolved, installsite.RetireRemedy(copy.Path).Text()} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
			if runtime.GOOS != "windows" && !strings.Contains(out.String(), "mv -n -- '/home/u/My Apps/sparkwing' '/home/u/My Apps/sparkwing.superseded'") {
				t.Errorf("remedy does not quote paths: %s", out.String())
			}
			for _, forbidden := range []string{"canonical", "upgrade", "downgrade", "older", "newer"} {
				if strings.Contains(out.String(), forbidden) {
					t.Errorf("unmeasured claim %q in %s", forbidden, out.String())
				}
			}
		})
	}
}

func TestDoctorCommandDistinguishesSurveyFailureFromGatedFleet(t *testing.T) {
	for _, format := range []string{"pretty", "plain", "json"} {
		for _, failed := range []bool{false, true} {
			deps := doctorTestDependencies(&doctorLifecycleFake{})
			deps.machine = func(context.Context) doctorReport {
				if failed {
					return doctorReport{GatesSurveyError: "read the repo registry: malformed config"}
				}
				return doctorReport{GatesSurveyed: 3, UngatedRepos: []githooks.RepoGates{{Repo: "/projects/repo", Missing: []string{"pre-push"}, Hooks: []githooks.HookObservation{{Name: "pre-push", Declared: true, Required: true, Active: true}}}}}
			}
			var out bytes.Buffer
			if err := runDoctorCommandArgs(t.Context(), []string{"-o", format}, &out, deps); err != nil {
				t.Fatal(err)
			}
			if failed {
				if !strings.Contains(out.String(), "malformed config") || strings.Contains(out.String(), "gates_surveyed\t") {
					t.Fatalf("%s hides survey failure: %s", format, out.String())
				}
				continue
			}
			for _, want := range []string{"gates_surveyed", "/projects/repo", "pre-push"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("%s missing %q in %s", format, want, out.String())
				}
			}
			if format != "json" && !strings.Contains(out.String(), "sparkwing pipeline hooks install --checkout /projects/repo") {
				t.Errorf("%s omits gate remedy: %s", format, out.String())
			}
		}
	}
}
