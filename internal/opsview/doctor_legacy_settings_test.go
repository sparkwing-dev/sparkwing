package opsview_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRenderDoctor_ACopiedLegacyFileIsANoticeNotARepair(t *testing.T) {
	report := opsview.DoctorReport{LegacySettings: []userconfig.Leftover{
		{Name: "/home/op/.config/sparkwing/fleet.yaml", MovesTo: "the fleet section of config.yaml", Copied: true},
	}}
	if !report.Clean() {
		t.Fatal("a copied legacy file left for older binaries made the report unclean")
	}
	var pretty bytes.Buffer
	if err := opsview.RenderDoctor(&pretty, report, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"notice: 1 legacy settings file(s) already copied", "/home/op/.config/sparkwing/fleet.yaml", "delete each once"} {
		if !strings.Contains(pretty.String(), want) {
			t.Errorf("pretty output lacks %q:\n%s", want, pretty.String())
		}
	}
}

func TestRenderDoctor_NamesLegacySettingsAndWhereTheyBelong(t *testing.T) {
	report := opsview.DoctorReport{
		LegacySettings: []userconfig.Leftover{
			{Name: "/home/op/.config/sparkwing/budget", MovesTo: "the admission.budget section of config.yaml"},
		},
		LegacySettingsError: "/home/op/.config/sparkwing/budget and the admission.budget section disagree",
	}
	if report.Clean() {
		t.Fatal("a report with a legacy setting left reads as clean")
	}
	var pretty, plain bytes.Buffer
	if err := opsview.RenderDoctor(&pretty, report, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/home/op/.config/sparkwing/budget -> the admission.budget section of config.yaml", "disagree"} {
		if !strings.Contains(pretty.String(), want) {
			t.Errorf("pretty output lacks %q:\n%s", want, pretty.String())
		}
	}
	if err := opsview.RenderDoctor(&plain, report, "plain", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.String(), "legacy_settings\t1\n") {
		t.Errorf("plain output lacks the legacy_settings count:\n%s", plain.String())
	}
}
