package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportHostedRunFailureAllowsAMissingHandle(t *testing.T) {
	output, err := exec.Command("bash", "report-hosted-run-failure.sh", filepath.Join(t.TempDir(), "missing.json"), "unused").CombinedOutput()
	if err != nil {
		t.Fatalf("missing handle diagnostics failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "run handle was not published") {
		t.Fatalf("missing handle diagnostics = %q", output)
	}
}

func TestReportHostedRunFailurePrintsStatusAndLogs(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	dir := t.TempDir()
	handle := filepath.Join(dir, "handle.json")
	if err := os.WriteFile(handle, []byte(`{"run_id":"run-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace")
	stub := filepath.Join(dir, "sparkwing")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$TRACE\"\n"
	if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "report-hosted-run-failure.sh", handle, stub)
	cmd.Env = append(os.Environ(), "TRACE="+trace)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hosted run diagnostics failed: %v\n%s", err, output)
	}
	got, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	want := "runs status --run run-fixture -o json\nruns logs --run run-fixture --tail 500 -o plain\n"
	if string(got) != want {
		t.Fatalf("diagnostic commands = %q, want %q", got, want)
	}
}
