package launcher

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
)

type loggingControl struct {
	*controlFake
	stop              context.CancelFunc
	claimErr, warmErr error
	calls             int
}

func (c *loggingControl) Claim(context.Context, string, []string) (*CloudJobAward, error) {
	c.calls++
	if c.calls == 2 {
		c.stop()
		return nil, nil
	}
	return c.award, c.claimErr
}

func (c *loggingControl) ReportWarmPools(context.Context, map[string]int64) error { return c.warmErr }

func TestLauncherLogsRecoverableRepliesAndFaults(t *testing.T) {
	for _, test := range []struct {
		name          string
		err           error
		level, reason string
	}{
		{"reply", controllerclient.ErrResponseNotObject, "INFO", "lease expiry"},
		{"fault", errors.New("connection refused"), "WARN", "launcher claim failed"},
		{"mixed fault", errors.Join(controllerclient.ErrResponseNotObject, errors.New("cleanup failed")), "WARN", "launcher claim failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, _, _, base := fixture()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				base.award = nil
				control := &loggingControl{controlFake: base, stop: cancel, claimErr: test.err, warmErr: test.err}
				var logs strings.Builder
				l.Control, l.Poll, l.Logger = control, time.Second, slog.New(slog.NewTextHandler(&logs, nil))
				if err := l.Run(ctx); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(logs.String(), "level="+test.level) || !strings.Contains(logs.String(), test.reason) {
					t.Fatal(logs.String())
				}
				if test.level == "INFO" && (!strings.Contains(logs.String(), "next complete sample") || strings.Contains(logs.String(), "level=WARN")) {
					t.Fatal(logs.String())
				}
			})
		})
	}
}

func TestLauncherLogsCapacityWaitOnceAndPreservesProvisioningFault(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	var logs strings.Builder
	l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	control.award = nil
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "waiting for provisioned capacity") != 1 || strings.Contains(logs.String(), "level=WARN") {
		t.Fatal(logs.String())
	}
	fault := errors.New("capacity API unavailable")
	kube.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fault })
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, fault) {
		t.Fatalf("provisioning fault hidden: %v", err)
	}
}
