package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOutputContractTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		args       []string
		code       int
	}{
		{"default", "", []string{"version", "--offline"}, 0},
		{"json", "json", []string{"version", "--offline"}, 0},
		{"plain", "plain", []string{"version", "--offline"}, 0},
		{"onboarding-json", "json", []string{"info", "--first-time"}, 0},
		{"help-json", "json", []string{"--help"}, 0},
		{"serve-running-default", "", []string{"serve", "start"}, 0},
		{"serve-running-json", "json", []string{"serve", "start"}, 0},
		{"serve-running-plain", "plain", []string{"serve", "start"}, 0},
		{"serve-default", "", []string{"serve", "stop"}, 0},
		{"serve-json", "json", []string{"serve", "stop"}, 0},
		{"serve-plain", "plain", []string{"serve", "stop"}, 0},
		{"consumer-default", "", []string{"runs", "consumer", "stop"}, 0},
		{"consumer-json", "json", []string{"runs", "consumer", "stop"}, 0},
		{"update-default", "", []string{"update", "--sdk", "--check"}, 2},
		{"update-json", "json", []string{"update", "--sdk", "--check"}, 2},
		{"update-plain", "plain", []string{"update", "--sdk", "--check"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
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
			args := append([]string{}, tc.args...)
			if strings.HasPrefix(tc.name, "serve-running") {
				dp, _, _ := dashboardSleepingRecord(t)
				args = append(args, "--home", dp.home)
			}
			if mode != "" {
				args = append(args, "--output", mode)
			}
			cmd := outputContractCommand(t, args...)
			cmd.Env = setEnv(cmd.Env, "NO_COLOR", "")
			cmd.Env = setEnv(cmd.Env, "CLICOLOR_FORCE", "1")
			cmd.Stdout = slave
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.code {
				t.Fatalf("terminal command: %v: %s", err, &stderr)
			}
			if err := slave.Close(); err != nil {
				t.Fatal(err)
			}
			out, err := io.ReadAll(master)
			if err != nil && !errors.Is(err, unix.EIO) {
				t.Fatal(err)
			}
			if mode == "json" {
				if bytes.Contains(out, []byte(`\u001b`)) || bytes.ContainsRune(out, '\x1b') {
					t.Fatalf("JSON contains terminal escapes: %q", out)
				}
				if len(decodeOutputRecords(t, out)) != 1 {
					t.Fatal("expected one JSON record")
				}
			} else if len(out) == 0 || bytes.HasPrefix(out, []byte("{")) {
				t.Fatalf("expected terminal text: %q", out)
			}
		})
	}
}
