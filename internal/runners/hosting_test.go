package runners

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func hostingAward(kind capability.UnitKind) controllerwire.ClaimedUnit {
	binding := capability.Binding{RunID: "run", Holder: "holder", Generation: 2, Kind: capability.BindingTrigger}
	purpose := capability.PurposeTrigger
	if kind == capability.UnitNode {
		binding.Kind, binding.NodeID, binding.Attempt = capability.BindingNode, "job", 3
		purpose = capability.PurposeWork
	}
	unit := controllerwire.ClaimedUnit{Unit: kind, Purpose: purpose, Delivery: capability.DeliveryDirect, UnitID: "unit", RunID: "run", JobID: binding.NodeID, Binding: binding, Capability: "award-secret", LeaseExpiresAt: time.Now().Add(time.Minute)}
	unit.Runner = nodeproto.RunnerIdentity{Name: "granted-runner", Type: "runner", Labels: []string{"granted"}}
	unit.Request = runspec.SealedRequest{RunID: unit.RunID, Hash: "request-hash", Request: runspec.Request{Team: "team"}}
	if kind == capability.UnitNode {
		unit.Engine = &engine.Identity{Version: "v0.20.0"}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash", Resolved: runspec.Resolved{Engine: *unit.Engine, SecretRegistration: runspec.SecretRegistration{Version: 1}}}, Plan: []byte(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
		unit.Execution = &runspec.JobExecutionSettings{}
	}
	return unit
}

type hostingRenewalClient struct {
	controllerwire.Controller
	renew func(context.Context) (lease.Beat, error)
}

func (c *hostingRenewalClient) Renew(ctx context.Context, _ string, _ controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	beat, err := c.renew(ctx)
	return controllerwire.RenewalResponse{CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason, Watched: beat.Watched, WatchedJobs: beat.WatchedNodes}, err
}

func hostingClient(renew func(context.Context) (lease.Beat, error)) func(client.EngineOptions) (controllerwire.Controller, error) {
	return func(client.EngineOptions) (controllerwire.Controller, error) {
		return &hostingRenewalClient{renew: renew}, nil
	}
}

func TestHostingFailuresPreventBinding(t *testing.T) {
	failure := errors.New("controller unavailable")
	for _, tc := range []struct {
		name string
		open func(client.EngineOptions) (controllerwire.Controller, error)
	}{
		{"connect", func(client.EngineOptions) (controllerwire.Controller, error) { return nil, failure }},
		{"no_client", func(client.EngineOptions) (controllerwire.Controller, error) { return nil, nil }},
		{"confirm", hostingClient(func(context.Context) (lease.Beat, error) { return lease.Beat{}, failure })},
		{"cancel", hostingClient(func(context.Context) (lease.Beat, error) {
			return lease.Beat{CancelRequested: true, CancelReason: "owner request"}, nil
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Hosting{
				ControllerURL: "https://controller.example", NewController: tc.open,
				Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
					t.Fatal("unauthorized award reached native binding")
					return nodehost.NativeServices{}, capability.Principal{}, nil
				},
			}
			err := h.Host(t.Context(), hostingAward(capability.UnitNode))
			if err == nil {
				t.Fatal("host accepted failed confirmation")
			}
			if tc.name == "cancel" {
				var cancelled *lease.CancelError
				if !errors.As(err, &cancelled) || cancelled.Reason != "owner request" {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestHostingPartialBindingClosesServices(t *testing.T) {
	bindErr, closeErr := errors.New("bind failed"), errors.New("close failed")
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			closed := 0
			h := Hosting{
				ControllerURL: "https://controller.example", NewController: hostingClient(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
				Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
					err := bindErr
					if cancelled {
						cancel(lease.ErrLeaseLost)
					}
					return nodehost.NativeServices{Close: func() error { closed++; return closeErr }}, capability.Principal{}, err
				},
			}
			err := h.Host(ctx, hostingAward(capability.UnitNode))
			want := bindErr
			if cancelled {
				want = lease.ErrLeaseLost
			}
			if !errors.Is(err, want) || !errors.Is(err, closeErr) || !errors.Is(err, bindErr) || closed != 1 {
				t.Fatalf("close=%d error=%v", closed, err)
			}
		})
	}
}

func TestHostingInterruptedConfirmationCannotBind(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	stop := errors.New("caller stopped")
	host := Hosting{ControllerURL: "https://controller.example", NewController: hostingClient(func(context.Context) (lease.Beat, error) { cancel(stop); return lease.Beat{}, nil }), Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
		t.Fatal("interrupted confirmation reached native binding")
		return nodehost.NativeServices{}, capability.Principal{}, nil
	}}
	if err := host.Host(ctx, hostingAward(capability.UnitTrigger)); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func FuzzHostingRejectsIncompleteNodeAwards(f *testing.F) {
	for missing := uint8(0); missing < 8; missing++ {
		f.Add(missing)
	}
	f.Fuzz(func(t *testing.T, missing uint8) {
		unit := hostingAward(capability.UnitNode)
		missing &= 7
		if missing&1 != 0 {
			unit.Accepted = nil
		}
		if missing&2 != 0 {
			unit.Engine = nil
		}
		if missing&4 != 0 {
			unit.Execution = nil
		}
		confirmed := false
		confirmation := errors.New("confirmed complete award")
		host := Hosting{
			ControllerURL: "https://controller.example",
			NewController: hostingClient(func(context.Context) (lease.Beat, error) {
				confirmed = true
				return lease.Beat{}, confirmation
			}),
			Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
				t.Fatal("award reached native binding")
				return nodehost.NativeServices{}, capability.Principal{}, nil
			},
		}
		err := host.Host(t.Context(), unit)
		if err == nil || confirmed != (missing == 0) || errors.Is(err, confirmation) != (missing == 0) {
			t.Fatalf("missing=%d confirmed=%v error=%v", missing, confirmed, err)
		}
	})
}
