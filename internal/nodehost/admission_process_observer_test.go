//go:build unix

package nodehost

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type processTrackingAdmission struct {
	daemon *fakeDaemon
	track  func(context.Context, int) error
}

func (a processTrackingAdmission) Acquire(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
	held, err := a.daemon.Acquire(ctx, request, queued)
	return processTrackingLease{Lease: held, track: a.track}, err
}

type processTrackingLease struct {
	Lease
	track func(context.Context, int) error
}

func (l processTrackingLease) TrackProcess(ctx context.Context, pid int) error {
	return l.track(ctx, pid)
}

func TestAdmissionRegistersSpawnedJobProcessBeforeProtocolRequests(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "registration failed"}[fail], func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			daemon := newFakeDaemon()
			registrationFailure := errors.New("process registration refused")
			observed := 0
			admission := LocalAdmission{Daemon: processTrackingAdmission{daemon: daemon, track: func(_ context.Context, pid int) error {
				observed = pid
				if fail {
					return registrationFailure
				}
				return nil
			}}}
			err = admission.node(t.Context(), localAdmissionSeal(t, localAdmissionOptions()), localAdmissionRow("job"), func(ctx context.Context) error {
				cfg := ProtocolSessionConfig{Command: []string{executable, "-test.run=^TestProtocolSessionChild$", "--", "normal"}, Environment: testProcessEnv(t, "GORACE=atexit_sleep_ms=0")}
				session, err := startProtocolSession(ctx, cfg)
				if session != nil {
					defer func() { _ = session.Close() }()
					if observed != session.cmd.Process.Pid || observed == os.Getpid() {
						t.Errorf("measuring PID %d, want spawned job PID %d", observed, session.cmd.Process.Pid)
					}
				}
				return err
			})
			if errors.Is(err, registrationFailure) != fail || !fail && err != nil || observed == 0 {
				t.Fatalf("observed=%d fail=%t error=%v", observed, fail, err)
			}
			if live, _, released := daemon.counts(); live != 0 || released != 1 {
				t.Fatalf("process registration leaked admission: live=%d released=%d", live, released)
			}
		})
	}
}
