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
			if err := runAdmin(os.Args[i+1:], os.Stdout, os.Stderr); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
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
			args := []string{"-test.run=^TestAdminOutputProcess$", "--", "session", "--operator", "ops-admin", "--team", "default", "--reason", "output contract"}
			if tc.mode != "" {
				args = append(args, "--output", tc.mode)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
			cmd.Env = append(os.Environ(), "SPARKWING_ADMIN_OUTPUT_HELPER=1", "HOME="+t.TempDir(), "CLICOLOR_FORCE=1")
			var output, audit bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &audit
			if tc.terminal {
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
				if err := cmd.Run(); err != nil {
					t.Fatalf("session command: %v", err)
				}
				if err := slave.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(&output, master); err != nil && !errors.Is(err, unix.EIO) {
					t.Fatal(err)
				}
			} else if err := cmd.Run(); err != nil {
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
