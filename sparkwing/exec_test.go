package sparkwing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestExecError_MessageIncludesCommandAndOutput(t *testing.T) {
	err := &ExecError{Command: "echo problem-one >&2 ; exit 1", Stderr: "problem-one", ExitCode: 1}
	msg := err.Error()
	if !strings.Contains(msg, "exit 1") {
		t.Fatalf("error missing exit: %q", msg)
	}
	if !strings.Contains(msg, "problem-one") {
		t.Fatalf("error missing stderr context: %q", msg)
	}
}

func TestExecError_MessageCapsALongCommandAndKeepsTheField(t *testing.T) {
	pattern := "^(" + strings.Repeat("TestSomethingLong|", 200) + "Té)$"
	command := "race.test -test.run " + pattern
	e := &ExecError{Command: command, ExitCode: 1, Stderr: "FAIL: TestSomethingLong"}
	msg := e.Error()
	if len(msg) > 700 || !strings.HasPrefix(msg, "command failed (exit 1): race.test -test.run ^(TestSomethingLong|") || !strings.Contains(msg, "more bytes]") || !strings.HasSuffix(msg, "\nFAIL: TestSomethingLong") {
		t.Fatalf("long command was not capped: %d bytes: %q", len(msg), msg)
	}
	if e.Command != command {
		t.Fatal("capping the message changed the Command field")
	}
	short := &ExecError{Command: "go test ./...", ExitCode: 2}
	if got := short.Error(); got != "command failed (exit 2): go test ./..." {
		t.Fatalf("short command changed: %q", got)
	}
}

func TestExecError_TerminatedRendersSignalNotStartFailure(t *testing.T) {
	e := &ExecError{
		Command:    "go test ./...",
		ExitCode:   ExitNotStarted,
		Cause:      errors.New("signal: killed"),
		Terminated: "cancellation",
	}
	msg := e.Error()
	if strings.Contains(msg, "command failed to start") {
		t.Fatalf("a terminated process must not read as a start failure: %q", msg)
	}
	want := "command terminated by cancellation: go test ./... (signal: killed)"
	if msg != want {
		t.Fatalf("Error() = %q, want %q", msg, want)
	}
}

func TestCommandRelaysArgumentsEnvironmentAndCapture(t *testing.T) {
	var got nodeproto.ExecRequest
	ctx := sdkserve.WithHostExec(t.Context(), func(_ context.Context, req nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
		got = req
		return sdkserve.HostExecResult{Done: nodeproto.ExecDone{ExitCode: 0}, Stdout: []byte("one\ntwo\n")}, nil
	})
	lines, err := Exec(ctx, "sh", "-c", "ignored").Dir("sub").Env("CLIENT_ENV", "value").Lines()
	if err != nil || len(lines) != 2 || lines[1] != "two" {
		t.Fatalf("result=%v error=%v", lines, err)
	}
	if !got.Capture || got.Dir != filepath.Join(WorkDir(), "sub") || len(got.Argv) != 3 {
		t.Fatalf("request=%+v", got)
	}
	found := false
	for _, entry := range got.Env {
		if entry == "CLIENT_ENV=value" {
			found = true
		}
	}
	if !found {
		t.Fatal("environment missing")
	}
}

func TestCommandRequiresHost(t *testing.T) {
	if _, err := Exec(t.Context(), "sh", "-c", "exit 0").Run(); err == nil {
		t.Fatal("command ran without a host")
	}
}
