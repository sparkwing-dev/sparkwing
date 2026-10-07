package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
)

func TestDoctorGoToolchainFormats(t *testing.T) {
	finding := &opsview.DoctorGoToolchain{Current: "go1.26.6", Setting: "local", Source: "/path/to/go/env", Floor: "1.26.8", Module: "/project/.sparkwing/go.mod", Verdict: "blocked by GOTOOLCHAIN=local", Error: "Install Go 1.26.8 or newer, or run 'go env -u GOTOOLCHAIN'."}
	report := opsview.DoctorReport{GoToolchain: finding}
	if report.Clean() {
		t.Fatal("blocked toolchain reported healthy")
	}
	for _, format := range []string{"pretty", "plain", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			if err := opsview.RenderDoctor(&out, report, format, ""); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var got opsview.DoctorReport
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.GoToolchain == nil || *got.GoToolchain != *finding {
					t.Fatalf("JSON finding = %+v", got.GoToolchain)
				}
				return
			}
			for _, want := range []string{finding.Current, finding.Setting, finding.Source, finding.Floor, finding.Verdict, finding.Error} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q: %s", want, out.String())
				}
			}
		})
	}
}

func TestDoctorGoToolchainAbsentOutsideProject(t *testing.T) {
	for _, format := range []string{"pretty", "plain", "json"} {
		var out bytes.Buffer
		if err := opsview.RenderDoctor(&out, opsview.DoctorReport{}, format, ""); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "go_toolchain") || strings.Contains(out.String(), "Go toolchain:") {
			t.Fatalf("%s invented a project finding: %s", format, out.String())
		}
	}
}
