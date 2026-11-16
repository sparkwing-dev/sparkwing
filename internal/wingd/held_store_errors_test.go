package wingd

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	protocol "github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestStandaloneAgainstARunningControllerNamesTheWayOut(t *testing.T) {
	runtime := newLifecycleRuntime()
	runtime.status = LocalStatus{Running: true, PID: 17}
	lifecycle := NewLocalLifecycle(runtime, nil)
	_, err := lifecycle.EnsureStarted(context.Background(), LocalStartOptions{Home: t.TempDir(), Standalone: true})
	var failure *protocol.Error
	if !errors.As(err, &failure) || failure.Code != protocol.Conflict {
		t.Fatalf("held store refusal = %#v; want a conflict", err)
	}
	for _, want := range []string{"sparkwing daemon stop", "SPARKWING_HOME"} {
		if !strings.Contains(failure.Hint, want) {
			t.Errorf("hint %q does not name %q", failure.Hint, want)
		}
	}
	if strings.Contains(failure.Message, "wingd:") {
		t.Errorf("message leaks the package name: %q", failure.Message)
	}
}

type presentDir struct{}

func (presentDir) Name() string       { return "system" }
func (presentDir) Size() int64        { return 0 }
func (presentDir) Mode() fs.FileMode  { return fs.ModeDir }
func (presentDir) ModTime() time.Time { return time.Time{} }
func (presentDir) IsDir() bool        { return true }
func (presentDir) Sys() any           { return nil }

func TestDaemonStopWithoutAUserBusSaysTheManagerIsUnreachable(t *testing.T) {
	host := runnerSupervisorHost(t)
	host.Stat = func(string) (fs.FileInfo, error) { return presentDir{}, nil }
	host.Exec = func(context.Context, string, ...string) (string, error) {
		return "Failed to connect to bus: No medium found", errors.New("exit status 1")
	}
	err := WaitLocalRunnerShutdown(t.Context(), host)
	var failure *protocol.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.Unavailable {
		t.Fatalf("no-bus stop = %#v; want an unavailable refusal", err)
	}
	if !strings.Contains(failure.Message, "the systemd user manager is unreachable (Failed to connect to bus") || !strings.Contains(failure.Hint, "loginctl enable-linger") {
		t.Fatalf("no-bus stop did not explain itself: %q / %q", failure.Message, failure.Hint)
	}
}
