package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOutputContractTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.6s of real work; the fast class runs under -short")
	}
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
