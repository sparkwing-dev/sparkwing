package nodehost

import (
	"os"
	"testing"
	"time"
)

func TestASessionReportsItsProcessAsItStarts(t *testing.T) {
	var started []int
	s, err := StartSession(t.Context(), SessionConfig{
		Entry: os.Args[0], Env: append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"),
		Sink: &recorder{}, Grace: 500 * time.Millisecond, Started: func(pid int) { started = append(started, pid) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.Close() }()
	if len(started) != 1 || started[0] != s.cmd.Process.Pid {
		t.Fatalf("started %v, want the SDK process %d", started, s.cmd.Process.Pid)
	}
}
