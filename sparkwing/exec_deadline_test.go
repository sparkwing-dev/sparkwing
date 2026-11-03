//go:build !windows

package sparkwing

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type deadlineCommandLogger struct {
	nopLogger
	cancel context.CancelCauseFunc
}

func (l deadlineCommandLogger) Emit(record LogRecord) {
	if record.Event == "exec_line" && len(record.streamData) > 0 {
		l.cancel(context.DeadlineExceeded)
	}
}

func TestExecDeadlineNamesTimeoutAndDeadline(t *testing.T) {
	deadline := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx, cancelDeadline := context.WithDeadline(t.Context(), deadline)
	defer cancelDeadline()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ctx = withTestLogger(ctx, deadlineCommandLogger{cancel: cancel})
	_, err := Bash(ctx, "printf ready; while :; do :; done").Run()
	var failure *ExecError
	var exit *exec.ExitError
	if !errors.As(err, &failure) || !errors.As(err, &exit) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline failure lost command or context cause: %v", err)
	}
	for _, want := range []string{"timed out", "2099-01-02T03:04:05Z", "increase the context timeout", "printf ready"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("deadline error %q omits %q", err, want)
		}
	}
}
