package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

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
