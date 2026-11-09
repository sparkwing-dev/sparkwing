package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportHostedRunFailureAllowsAMissingHandle(t *testing.T) {
	output, err := exec.Command("bash", "report-hosted-run-failure.sh", filepath.Join(t.TempDir(), "missing.json"), "unused", filepath.Join(t.TempDir(), "run.log")).CombinedOutput()
	if err != nil {
		t.Fatalf("missing handle diagnostics failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "no run handle is available; cannot locate stored diagnostics") {
		t.Fatalf("missing handle diagnostics = %q", output)
	}
}

func TestReportHostedRunFailureRetainsFullLogs(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	for _, tc := range []struct {
		name                string
		statusExit, logExit int
	}{
		{name: "complete"},
		{name: "status unavailable", statusExit: 7},
		{name: "partial logs", logExit: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			handle := filepath.Join(dir, "handle.json")
			if err := os.WriteFile(handle, []byte(`{"run_id":"run-fixture"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(dir, "trace")
			stub := filepath.Join(dir, "sparkwing")
			body := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >>"$TRACE"
if [ "$2" = status ]; then
 echo 'stored status'
 exit %d
fi
seq 1 650
exit %d
`, tc.statusExit, tc.logExit)
			if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "retained run.log")
			cmd := exec.Command("bash", "report-hosted-run-failure.sh", handle, stub, log)
			cmd.Env = append(os.Environ(), "TRACE="+trace)
			output, err := cmd.CombinedOutput()
			wantExit := tc.logExit
			if wantExit == 0 {
				wantExit = tc.statusExit
			}
			if cmd.ProcessState.ExitCode() != wantExit {
				t.Fatalf("exit = %d, want %d: %v\n%s", cmd.ProcessState.ExitCode(), wantExit, err, output)
			}
			retained, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("full run log was not retained: %v", err)
			}
			if !strings.HasPrefix(string(retained), "1\n2\n") || !strings.HasSuffix(string(retained), "650\n") || strings.Count(string(retained), "\n") != 650 {
				t.Fatalf("retained log lost output: %q", retained)
			}
			got, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			want := "runs status --run run-fixture -o json\nruns logs --run run-fixture -o plain\n"
			if string(got) != want {
				t.Fatalf("diagnostic commands = %q, want %q", got, want)
			}
			if strings.Contains(string(output), "\n1\n") || !strings.Contains(string(output), "\n151\n") || !strings.HasSuffix(string(output), "650\n") {
				t.Fatalf("console must retain only the last 500 log lines: %s", output)
			}
		})
	}
}
