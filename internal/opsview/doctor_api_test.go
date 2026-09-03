package opsview

import (
	"encoding/json"
	"strings"
	"testing"
)

func apiReady(v bool) *bool { return &v }

func TestDoctorJSONCarriesAPIReadyWhenFalse(t *testing.T) {
	d := DoctorDaemon{State: ReachServing, Reachable: true, Socket: "/tmp/d.sock",
		APISocket: "/tmp/api.sock", APIReady: apiReady(false), APIError: "permission denied"}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if ready, present := decoded["api_ready"]; !present || ready != false {
		t.Fatalf("api_ready = %v present=%v in %s, want an explicit false", ready, present, raw)
	}
}

func TestDoctorReportIsNotCleanWithAnUnboundAPISocket(t *testing.T) {
	report := DoctorReport{Daemon: DoctorDaemon{State: ReachServing, Reachable: true,
		APISocket: "/tmp/api.sock", APIReady: apiReady(false), APIError: "permission denied"}}
	if report.Clean() {
		t.Fatal("a daemon serving admission with no controller API reports a clean machine")
	}
	report.Daemon.APIReady = apiReady(true)
	if !report.Clean() {
		t.Fatal("a daemon serving both sockets reports an unclean machine")
	}
	report.Daemon.APIReady = nil
	if !report.Clean() {
		t.Fatal("a daemon that does not report api_ready is judged on it")
	}
}

func TestDoctorPrettyNamesTheAPIFault(t *testing.T) {
	var sb strings.Builder
	renderDaemonSection(&sb, DoctorReport{Daemon: DoctorDaemon{
		State: ReachServing, Reachable: true, Version: "v1", Socket: "/tmp/d.sock",
		APISocket: "/tmp/api.sock", APIReady: apiReady(false), APIError: "permission denied",
	}})
	out := sb.String()
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("output %q does not name the bind failure", out)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("output %q reports an unbound API socket without a warning", out)
	}

	sb.Reset()
	renderDaemonSection(&sb, DoctorReport{Daemon: DoctorDaemon{
		State: ReachServing, Reachable: true, Version: "v1", Socket: "/tmp/d.sock",
		APISocket: "/tmp/api.sock",
	}})
	if strings.Contains(sb.String(), "controller API") {
		t.Fatalf("output %q names the controller API for a daemon that does not report it", sb.String())
	}
}
