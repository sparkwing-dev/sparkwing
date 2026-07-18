package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
