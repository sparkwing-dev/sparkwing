package opsview

import (
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestDiagnoseToolchains_ListsEveryStoredRelease(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	for version, body := range map[string]string{
		"v0.40.0": "forty",
		"v0.39.0": "thirty-nine",
	} {
		if err := os.MkdirAll(p.ToolchainDir(version), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.ToolchainBinary(version), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(p.ToolchainDir("v0.38.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseToolchains(p, &report)

	if len(report.Toolchains) != 2 {
		t.Fatalf("toolchains = %+v, want the two directories holding a binary", report.Toolchains)
	}
	if report.Toolchains[0].Version != "v0.39.0" || report.Toolchains[1].Version != "v0.40.0" {
		t.Fatalf("toolchains are not ordered by version: %+v", report.Toolchains)
	}
	if report.Toolchains[1].Bytes != int64(len("forty")) {
		t.Errorf("size = %d, want %d", report.Toolchains[1].Bytes, len("forty"))
	}
	if report.Toolchains[1].Path != p.ToolchainBinary("v0.40.0") {
		t.Errorf("path = %q, want %q", report.Toolchains[1].Path, p.ToolchainBinary("v0.40.0"))
	}

	var out strings.Builder
	renderToolchains(&out, report)
	for _, want := range []string{"toolchain store", "v0.39.0", "v0.40.0", p.ToolchainBinary("v0.40.0")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rendered store %q does not contain %q", out.String(), want)
		}
	}
}

func TestDiagnoseToolchains_SaysNothingWithoutAStore(t *testing.T) {
	var report DoctorReport
	diagnoseToolchains(paths.PathsAt(t.TempDir()), &report)
	if len(report.Toolchains) != 0 {
		t.Fatalf("toolchains = %+v, want none", report.Toolchains)
	}
	var out strings.Builder
	renderToolchains(&out, report)
	if out.Len() != 0 {
		t.Fatalf("rendered %q for an empty store", out.String())
	}
}
