package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
)

func TestDoctorReport_RepeatRejectionsAreNotClean(t *testing.T) {
	r := opsview.DoctorReport{
		AdmissionRejections: []opsview.DoctorRejection{{Cause: "cost_source", Count: 4}},
	}
	if r.Clean() {
		t.Fatal("report with repeat admission rejections reported clean")
	}
}

func TestRenderDoctorPretty_ExplainsRepeatRejections(t *testing.T) {
	r := opsview.DoctorReport{
		AdmissionRejections: []opsview.DoctorRejection{{Cause: "cost_source", Count: 4}},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "4 admission request(s) rejected as invalid") {
		t.Errorf("pretty output missing rejection count line:\n%s", out)
	}
	if !strings.Contains(out, "cost_source") || !strings.Contains(out, "cost source") {
		t.Errorf("pretty output does not name and explain the cause:\n%s", out)
	}
	if strings.Contains(out, "healthy") {
		t.Errorf("a report with rejections should not read healthy:\n%s", out)
	}
}

func TestDoctorReport_VersionSkewIsNotClean(t *testing.T) {
	r := opsview.DoctorReport{
		DaemonVersionSkew: &opsview.DoctorVersionSkew{Self: "(devel)", Daemon: "v0.18.0"},
	}
	if r.Clean() {
		t.Fatal("report with a daemon version skew reported clean")
	}
}

func TestRenderDoctorPretty_ExplainsVersionSkew(t *testing.T) {
	r := opsview.DoctorReport{
		DaemonVersionSkew: &opsview.DoctorVersionSkew{Self: "(devel)", Daemon: "v0.18.0"},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "version skew") || !strings.Contains(out, "(devel)") || !strings.Contains(out, "v0.18.0") {
		t.Errorf("pretty output does not explain the skew with both versions:\n%s", out)
	}
	if strings.Contains(out, "healthy") {
		t.Errorf("a report with a version skew should not read healthy:\n%s", out)
	}
}

func TestDoctorReport_QuarantinedLedgersAreNotClean(t *testing.T) {
	r := opsview.DoctorReport{
		QuarantinedLedgers: []string{"/home/.sparkwing/wingd/state.json.corrupt-1784666506"},
	}
	if r.Clean() {
		t.Fatal("report with quarantined ledgers reported clean")
	}
}

func TestRenderDoctorPretty_ListsQuarantinedLedgers(t *testing.T) {
	r := opsview.DoctorReport{
		QuarantinedLedgers: []string{"/home/.sparkwing/wingd/state.json.corrupt-1784666506"},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "state.json.corrupt-1784666506") || !strings.Contains(out, "could not restore") {
		t.Errorf("pretty output does not name and explain the quarantined ledger:\n%s", out)
	}
	if !strings.Contains(out, "safe to delete") {
		t.Errorf("pretty output does not tell the operator the file is deletable:\n%s", out)
	}
	if strings.Contains(out, "healthy") {
		t.Errorf("a report with quarantined ledgers should not read healthy:\n%s", out)
	}
}

func TestDoctorReport_PoisonedProfilesAreNotClean(t *testing.T) {
	r := opsview.DoctorReport{
		PoisonedProfiles: []opsview.DoctorPoisonedProfile{
			{Pipeline: "myrepo/ci", FloorCores: 6.9, ChargeCores: 13.8, GrantableCores: 7.5},
		},
	}
	if r.Clean() {
		t.Fatal("report with a poisoned capacity profile reported clean")
	}
}

func TestRenderDoctorPretty_NamesPoisonedProfileAndReset(t *testing.T) {
	r := opsview.DoctorReport{
		PoisonedProfiles: []opsview.DoctorPoisonedProfile{
			{Pipeline: "myrepo/ci", FloorCores: 6.9, ChargeCores: 13.8, GrantableCores: 7.5},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"myrepo/ci"`) || !strings.Contains(out, "poisoned by contention") {
		t.Errorf("pretty output does not name the poisoned profile:\n%s", out)
	}
	if !strings.Contains(out, "sparkwing runs stats --reset --pipeline myrepo/ci") {
		t.Errorf("pretty output does not carry the exact reset command:\n%s", out)
	}
	if strings.Contains(out, "healthy") {
		t.Errorf("a report with a poisoned profile should not read healthy:\n%s", out)
	}
}

func TestDoctorReport_ShadowedHooksAreNotClean(t *testing.T) {
	r := opsview.DoctorReport{ShadowedHooks: &githooks.Shadow{Gates: []string{"pre-push"}}}
	if r.Clean() {
		t.Fatal("report with a shadowed hook directory reported clean")
	}
}

func TestRenderDoctorPretty_ExplainsShadowedHooks(t *testing.T) {
	r := opsview.DoctorReport{
		ShadowedHooks: &githooks.Shadow{
			Repo:      "/home/dev/proj",
			HooksDir:  "/home/dev/proj/.git/hooks",
			ActiveDir: "/home/dev/.config/git/hooks",
			Scope:     "global",
			Gates:     []string{"pre-commit", "pre-push"},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "/home/dev/.config/git/hooks") || !strings.Contains(out, "/home/dev/proj/.git/hooks") {
		t.Errorf("pretty output does not name both hook directories:\n%s", out)
	}
	if !strings.Contains(out, "pre-commit, pre-push") {
		t.Errorf("pretty output does not name the gates that stopped firing:\n%s", out)
	}
	if !strings.Contains(out, "sparkwing pipeline hooks install") {
		t.Errorf("pretty output does not carry the fix:\n%s", out)
	}
	if strings.Contains(out, "healthy") {
		t.Errorf("a report with shadowed hooks should not read healthy:\n%s", out)
	}
}

func TestRenderDoctorJSON_CarriesShadowedHooks(t *testing.T) {
	r := opsview.DoctorReport{
		ShadowedHooks: &githooks.Shadow{
			Repo:      "/home/dev/proj",
			HooksDir:  "/home/dev/proj/.git/hooks",
			ActiveDir: "/home/dev/.config/git/hooks",
			Scope:     "global",
			Gates:     []string{"pre-push"},
		},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "json", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	var got opsview.DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ShadowedHooks == nil || got.ShadowedHooks.Scope != "global" ||
		len(got.ShadowedHooks.Gates) != 1 || got.ShadowedHooks.Gates[0] != "pre-push" {
		t.Errorf("round-tripped shadowed hooks = %+v, want the global-scope pre-push finding", got.ShadowedHooks)
	}
}

func TestRenderDoctorJSON_CarriesRejections(t *testing.T) {
	r := opsview.DoctorReport{
		AdmissionRejections: []opsview.DoctorRejection{{Cause: "request", Count: 3}},
	}
	var buf bytes.Buffer
	if err := opsview.RenderDoctor(&buf, r, "json", ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	var got opsview.DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.AdmissionRejections) != 1 || got.AdmissionRejections[0].Cause != "request" ||
		got.AdmissionRejections[0].Count != 3 {
		t.Errorf("round-tripped rejections = %+v, want request:3", got.AdmissionRejections)
	}
}
