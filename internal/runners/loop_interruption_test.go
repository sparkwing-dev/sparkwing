package runners

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

func FuzzLoopPendingPolicyInterruption(f *testing.F) {
	for mode := range uint8(5) {
		f.Add("shutdown", mode, []byte{0}, false, false)
		f.Add("shutdown", mode, []byte{1, 2, 3, 4}, true, true)
		f.Add("shutdown", mode, []byte{3}, true, false)
		f.Add("shutdown", mode+8, []byte{}, true, false)
	}
	f.Fuzz(func(t *testing.T, message string, mode uint8, tree []byte, awarded, hostFails bool) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var cause error = errors.New(message)
			if mode%5 == 2 || mode%5 == 4 {
				cause = pendingPolicyCause{message}
			}
			if mode%5 >= 3 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeoutCause(ctx, 2*limits.VersionIndexRefresh, cause)
				defer stop()
			}
			denied, hostErr := errors.New("independent denial"), errors.New("host failed")
			refreshing := make(chan struct{})
			checks, hosted := 0, 0
			wantDenied := false
			loop := Loop{
				Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1},
				CheckVersion: func(policyCtx context.Context, _ string) error {
					checks++
					if checks == 1 {
						return nil
					}
					close(refreshing)
					<-policyCtx.Done()
					var err error = policyCtx.Err()
					if mode&8 != 0 {
						err = context.Cause(policyCtx)
					}
					for _, shape := range tree[:min(len(tree), 24)] {
						switch shape % 5 {
						case 0:
							err = fmt.Errorf("policy: %w", err)
						case 1:
							err = errors.Join(err, policyCtx.Err())
						case 2:
							err = errors.Join(denied, err)
							wantDenied = true
						case 3:
							err = errors.Join(err, context.Cause(policyCtx))
						default:
							err = errors.Join(err, nil)
						}
					}
					return err
				},
				Controller: claimFunc(func(claimCtx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					<-claimCtx.Done()
					if awarded {
						return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
					}
					return controllerwire.ClaimResponse{}, claimCtx.Err()
				}),
				Host: hostFunc(func(hostCtx context.Context, _ controllerwire.ClaimedUnit) error {
					hosted++
					if (hostCtx.Err() != nil) != (mode%5 != 0) {
						t.Error("host context does not reflect caller cancellation")
					}
					if hostFails {
						return hostErr
					}
					return nil
				}),
			}
			finished := make(chan error, 1)
			go func() {
				defer func() {
					if value := recover(); value != nil {
						finished <- fmt.Errorf("Run panicked: %v", value)
					}
				}()
				finished <- loop.Run(ctx)
			}()
			<-refreshing
			if mode%5 == 0 {
				loop.Drain()
			} else if mode%5 < 3 {
				cancel(cause)
			}
			err := <-finished
			wantHosted := 0
			if awarded {
				wantHosted = 1
			}
			if hosted != wantHosted || errors.Is(err, denied) != wantDenied || errors.Is(err, hostErr) != (awarded && hostFails) {
				t.Fatalf("awarded=%t hosted=%d denied=%t hostFails=%t error=%v", awarded, hosted, wantDenied, hostFails, err)
			}
			if (mode%5 == 1 || mode%5 == 3) && !errors.Is(err, cause) {
				t.Fatalf("caller cause lost: %v", err)
			}
			if mode%5 == 2 || mode%5 == 4 {
				var got pendingPolicyCause
				if !errors.As(err, &got) || len(got) != 1 || got[0] != message {
					t.Fatalf("slice-backed caller cause lost: %v", err)
				}
			}
			if mode%5 == 0 && !wantDenied && !(awarded && hostFails) && err != nil {
				t.Fatalf("clean drain returned %v", err)
			}
		})
	})
}
