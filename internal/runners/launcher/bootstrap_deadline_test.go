package launcher

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func FuzzBootstrapRespectsAwardDeadline(f *testing.F) {
	for kind := range uint8(3) {
		f.Add(kind, int16(30), int16(40), int16(50))
		f.Add(kind, int16(0), int16(0), int16(30))
		f.Add(kind, int16(-1), int16(10), int16(30))
		f.Add(kind, int16(10), int16(10), int16(-1))
	}
	f.Fuzz(func(t *testing.T, kind uint8, owner, attempt, parent int16) {
		award, unit := bootstrapAward(t, []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kind%3])
		now := time.Now()
		bound := func(seconds int16) time.Time {
			if seconds == 0 {
				return time.Time{}
			}
			return now.Add(time.Duration(seconds) * time.Second)
		}
		award.Binding.OwnerDeadline, award.Binding.AttemptDeadline, award.Binding.ParentCapabilityExpiry = bound(owner), bound(attempt), bound(parent)
		unit.OwnerDeadline, unit.AttemptDeadline, unit.ParentCapabilityExpiry = &award.Binding.OwnerDeadline, &award.Binding.AttemptDeadline, &award.Binding.ParentCapabilityExpiry
		want, invalid := limits.JobDeadline(now, bound(owner), bound(attempt))
		if parent <= 0 {
			invalid = limits.ErrNoTime
		} else if want.IsZero() || bound(parent).Before(want) {
			want = bound(parent)
		}
		callerEnd := now.Add(15 * time.Second)
		if invalid == nil && callerEnd.Before(want) {
			want = callerEnd
		}
		ctx, cancel := context.WithDeadline(t.Context(), callerEnd)
		defer cancel()
		connects, exchanges, executions := 0, 0, 0
		check := func(ctx context.Context) {
			t.Helper()
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.Equal(want) {
				t.Fatalf("award deadline: got %v, want %v", deadline, want)
			}
		}
		host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			connects++
			return bootstrapController{exchange: func(ctx context.Context) (controllerwire.ExchangeResponse, error) {
				exchanges++
				check(ctx)
				return controllerwire.ExchangeResponse{Claim: unit}, nil
			}}, nil
		}, Host: bootstrapUnitHost(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
			executions++
			check(ctx)
			return nil
		})}
		err := host.Run(ctx, award)
		if invalid != nil {
			if !errors.Is(err, limits.ErrNoTime) || connects+exchanges+executions != 0 {
				t.Fatalf("expired award reached transport: %d/%d/%d: %v", connects, exchanges, executions, err)
			}
		} else if err != nil || connects != 1 || exchanges != 1 || executions != 1 {
			t.Fatalf("live award: %d/%d/%d: %v", connects, exchanges, executions, err)
		}
	})
}

func TestBootstrapDeadlineCancelsExchangeAndHost(t *testing.T) {
	for _, phase := range []string{"exchange", "host"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				award, unit := bootstrapAward(t, capability.UnitNode)
				award.Binding.ParentCapabilityExpiry = time.Now().Add(time.Second)
				unit.ParentCapabilityExpiry = &award.Binding.ParentCapabilityExpiry
				executed := false
				host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
					return bootstrapController{exchange: func(ctx context.Context) (controllerwire.ExchangeResponse, error) {
						if phase == "exchange" {
							<-ctx.Done()
						}
						return controllerwire.ExchangeResponse{Claim: unit}, nil
					}}, nil
				}, Host: bootstrapUnitHost(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
					executed = true
					<-ctx.Done()
					return nil
				})}
				if err := host.Run(t.Context(), award); !errors.Is(err, context.DeadlineExceeded) || executed != (phase == "host") {
					t.Fatalf("deadline failed to join %s: executed=%t: %v", phase, executed, err)
				}
			})
		})
	}
}
