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

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLauncherClaimPollTiming(t *testing.T) {
	for _, test := range []struct {
		name                            string
		latency, response, advice, want time.Duration
		syncFailure                     bool
	}{
		{name: "variable admission latency", latency: 200 * time.Millisecond, advice: 2 * time.Second, want: 2 * time.Second},
		{name: "response completion", response: 300 * time.Millisecond, advice: 2 * time.Second, want: 2 * time.Second},
		{name: "longer controller suggestion", advice: 5 * time.Second, want: 5 * time.Second},
		{name: "suggestion survives reconciliation failure", advice: 5 * time.Second, want: 5 * time.Second, syncFailure: true},
		{name: "configured poll floor", advice: time.Second, want: 2 * time.Second},
		{name: "missing advice", want: 2 * time.Second},
		{name: "negative advice", advice: -time.Second, want: 2 * time.Second},
		{name: "supported advice ceiling", advice: time.Hour, want: controllerwire.MaxHonoredIdleClaimPoll},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, _ := fixture()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				gate := ratelimit.New(1, test.want)
				var admissions []time.Time
				limited := 0
				passes := 0
				kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					passes++
					if test.syncFailure && passes == 2 {
						return true, nil, errors.New("temporary Kubernetes failure")
					}
					return false, nil, nil
				})
				wire := launcherWireFake{
					claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						if len(admissions) == 0 {
							time.Sleep(test.latency)
						}
						now := time.Now()
						admissions = append(admissions, now)
						if len(admissions) == 2 {
							cancel()
						}
						if !gate.Allow("launcher", now) {
							limited++
							return controllerwire.ClaimResponse{}, &apierr.Error{Code: hosterr.RateLimited, Message: "poll again no sooner than the interval this controller suggested"}
						}
						if len(admissions) == 1 {
							time.Sleep(test.response)
						}
						return controllerwire.ClaimResponse{PollAfter: test.advice}, nil
					},
					sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
						return controllerwire.LauncherSyncResponse{}, nil
					},
				}
				control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
				if err != nil {
					t.Fatal(err)
				}
				var logs strings.Builder
				l.Control, l.Poll, l.Logger = control, 2*time.Second, slog.New(slog.NewTextHandler(&logs, nil))
				if err := l.Run(ctx); err != nil {
					t.Fatal(err)
				}
				if len(admissions) != 2 {
					t.Fatalf("admissions=%v", admissions)
				}
				spacing := admissions[1].Sub(admissions[0])
				want := test.want + test.response
				if limited != 0 || strings.Contains(logs.String(), "launcher claim failed") || spacing != want {
					t.Fatalf("admission spacing=%s want=%s rate_limited=%d logs=%s", spacing, want, limited, logs.String())
				}
				if test.want > l.Poll && passes < 3 {
					t.Fatalf("reconciliation paused during claim cooldown: %d passes", passes)
				}
			})
		})
	}
}
