//go:build e2e

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceOutputStoppedRoutes(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		service string
		code    int
	}{
		{[]string{"serve", "status"}, "dashboard", 1},
		{[]string{"serve", "stop"}, "dashboard", 0},
		{[]string{"runs", "consumer", "status"}, "consumer", 1},
		{[]string{"runs", "consumer", "stop"}, "consumer", 0},
	} {
		for _, mode := range []string{"", "json", "plain", "pretty"} {
			t.Run(strings.Join(tc.args, " ")+"/"+mode, func(t *testing.T) {
				args := append([]string{}, tc.args...)
				if mode != "" {
					args = append(args, "--output", mode)
				}
				cmd := outputContractCommand(t, args...)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if cmd.ProcessState.ExitCode() != tc.code {
					t.Fatalf("exit: %v: %s", err, &stderr)
				}
				switch mode {
				case "", "json":
					records := decodeOutputRecords(t, out)
					if len(records) != 1 || records[0]["service"] != tc.service || records[0]["state"] != "stopped" || records[0]["home"] == "" {
						t.Fatalf("status: %s", out)
					}
				case "plain":
					if string(out) != "stopped\n" {
						t.Fatalf("plain: %q", out)
					}
				case "pretty":
					if !strings.Contains(string(out), "not running") && !strings.Contains(string(out), "stopped") {
						t.Fatalf("pretty: %q", out)
					}
				}
			})
		}
	}
}
