package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAdminOutputProcess(t *testing.T) {
	if os.Getenv("SPARKWING_ADMIN_OUTPUT_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0], "admin"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestAdminSessionPipeAndTerminal(t *testing.T) {
	st, _, _ := adminSessionFixture(t)
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','ops-admin','owner',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode string
		terminal   bool
		json       bool
	}{
		{"pipe-default", "", false, true},
		{"terminal-default", "", true, false},
		{"pipe-plain", "plain", false, false},
		{"pipe-pretty", "pretty", false, false},
		{"terminal-json", "json", true, true},
		{"terminal-plain", "plain", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"session", "--operator", "ops-admin", "--team", "default", "--reason", "output contract"}
			if tc.mode != "" {
				args = append(args, "--output", tc.mode)
			}
			output, audit, err := adminOutputCommand(t, tc.terminal, args...)
			if err != nil {
				t.Fatalf("session command: %v", err)
			}
			raw := strings.TrimSpace(output.String())
			if tc.json {
				var result struct {
					Session string `json:"session"`
				}
				if err := json.Unmarshal(output.Bytes(), &result); err != nil || bytes.Count(output.Bytes(), []byte("\n")) != 1 {
					t.Fatal("expected one complete JSON session record")
				}
				raw = result.Session
			} else if json.Valid(output.Bytes()) {
				t.Fatal("expected session text")
			}
			if raw == "" || strings.ContainsAny(raw, "\r\n\x1b") || strings.Contains(audit.String(), raw) {
				t.Fatal("session missing, decorated or leaked to audit")
			}
		})
	}
}

func adminOutputCommand(t *testing.T, terminal bool, args ...string) (bytes.Buffer, bytes.Buffer, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestAdminOutputProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "SPARKWING_ADMIN_OUTPUT_HELPER=1", "HOME="+t.TempDir(), "CLICOLOR_FORCE=1")
	var output, audit bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &audit
	if !terminal {
		err := cmd.Run()
		return output, audit, err
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	cmd.Stdout = slave
	runErr := cmd.Run()
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&output, master); err != nil && !errors.Is(err, unix.EIO) {
		t.Fatal(err)
	}
	return output, audit, runErr
}

func TestAdminMutationPipeAndTerminal(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := openState(t).Close(); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"freeze", "unfreeze", "grant"} {
		var grantID string
		for _, tc := range []struct {
			name, mode string
			terminal   bool
		}{
			{"pipe-default", "", false},
			{"terminal-default", "", true},
			{"pipe-pretty", "pretty", false},
			{"pipe-plain", "plain", false},
			{"terminal-json", "json", true},
			{"terminal-plain", "plain", true},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				args := []string{verb, "--operator=ops-admin", "--team=default", "--reason=output contract"}
				if verb == "grant" {
					args = append(args, "--cents=1", "--key=output-contract")
				}
				if tc.mode != "" {
					args = append(args, "-o="+tc.mode)
				}
				out, audit, err := adminOutputCommand(t, tc.terminal, args...)
				if err != nil {
					t.Fatalf("%v: %s", err, audit.String())
				}
				if tc.mode == "json" || tc.mode == "" && !tc.terminal {
					var result map[string]any
					if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["team"] != "default" || bytes.Count(out.Bytes(), []byte("\n")) != 1 {
						t.Fatalf("expected one JSON mutation record: %q, %v", out.String(), err)
					}
					if verb == "grant" {
						grantID, _ = result["grant_id"].(string)
					}
				} else if tc.mode == "plain" {
					value := strings.TrimSpace(out.String())
					if value == "" || strings.ContainsAny(value, "\r\n\x1b") || verb != "grant" && value != "default" || verb == "grant" && value != grantID {
						t.Fatalf("expected primary value: %q", value)
					}
				} else if !strings.Contains(out.String(), "Team: default") || json.Valid(out.Bytes()) {
					t.Fatalf("expected pretty mutation: %q", out.String())
				}
			})
		}
	}
}

func TestAdminHelpPipeAndTerminal(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	for _, verb := range []string{"session", "freeze", "unfreeze", "grant"} {
		for _, terminal := range []bool{false, true} {
			for _, flags := range [][]string{{"--help"}, {"--help", "--output=json"}, {"-o", "plain", "-h"}, {"--help", "-o=pretty"}} {
				out, diagnostic, err := adminOutputCommand(t, terminal, append([]string{verb}, flags...)...)
				if err != nil || diagnostic.Len() != 0 {
					t.Fatalf("%s %v terminal=%t: %v, %s", verb, flags, terminal, err, diagnostic.String())
				}
				if len(flags) == 1 && !terminal || flags[len(flags)-1] == "--output=json" {
					var record struct{ Kind, Text string }
					if err := json.Unmarshal(out.Bytes(), &record); err != nil || record.Kind != "help" || !strings.Contains(record.Text, "--session-stdin") || bytes.Count(out.Bytes(), []byte("\n")) != 1 {
						t.Fatalf("expected JSON help: %q, %v", out.String(), err)
					}
				} else if !strings.HasPrefix(out.String(), "usage:") {
					t.Fatalf("expected text help: %q", out.String())
				}
			}
			for _, flags := range [][]string{{"--help", "--output=xml"}, {"--help", "-o="}, {"--help", "--output"}, {"--output=xml", "--help"}} {
				out, diagnostic, err := adminOutputCommand(t, terminal, append([]string{verb}, flags...)...)
				if err == nil || out.Len() != 0 || diagnostic.Len() == 0 {
					t.Fatalf("%s %v: invalid output with help must fail cleanly", verb, flags)
				}
			}
		}
	}
}
