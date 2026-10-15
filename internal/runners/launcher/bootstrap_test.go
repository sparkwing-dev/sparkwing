package launcher

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type bootstrapController struct {
	controllerwire.Controller
	exchange func(context.Context) (controllerwire.ExchangeResponse, error)
}

func (c bootstrapController) Exchange(ctx context.Context) (controllerwire.ExchangeResponse, error) {
	return c.exchange(ctx)
}

type bootstrapUnitHost func(context.Context, controllerwire.ClaimedUnit) error

func (h bootstrapUnitHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return h(ctx, unit)
}

func bootstrapAward(t testing.TB, kind capability.UnitKind) (CloudJobAward, controllerwire.ClaimedUnit) {
	t.Helper()
	unit := wireAward(kind)
	binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	award := CloudJobAward{Binding: binding, Bootstrap: []byte(unit.Capability), Engine: unit.Engine, CPUCores: 1.5, Memory: 2 << 30}
	unit.Delivery, unit.Capability = capability.DeliveryDirect, "private-work-award"
	unit.LeaseExpiresAt = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	return award, unit
}

func TestBootstrapExchangesOnceBeforeDispatchingOriginalUnit(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			award, unit := bootstrapAward(t, kind)
			failure := errors.New("host result")
			var events []string
			host := BootstrapHost{ControllerURL: "https://controller.example", HTTP: &http.Client{}}
			host.NewController = func(options client.EngineOptions) (controllerwire.Controller, error) {
				events = append(events, "connect")
				bearer, err := options.Bearer(t.Context())
				if err != nil || bearer != string(award.Bootstrap) || options.URL != host.ControllerURL || options.HTTP != host.HTTP || options.Launcher {
					t.Fatal("exchange transport did not retain bootstrap-only authority")
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if raw, err := options.Bearer(ctx); raw != "" || !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled lookup returned authority")
				}
				return bootstrapController{exchange: func(ctx context.Context) (controllerwire.ExchangeResponse, error) {
					if ctx != t.Context() {
						t.Fatal("exchange changed caller context")
					}
					events = append(events, "exchange")
					return controllerwire.ExchangeResponse{Claim: unit}, nil
				}}, nil
			}
			host.Host = bootstrapUnitHost(func(ctx context.Context, got controllerwire.ClaimedUnit) error {
				events = append(events, "host")
				if ctx != t.Context() || !reflect.DeepEqual(got, unit) {
					t.Fatal("host lost the exchanged unit, runner, engine or source fence")
				}
				return failure
			})
			if err := host.Run(t.Context(), award); !errors.Is(err, failure) || !reflect.DeepEqual(events, []string{"connect", "exchange", "host"}) {
				t.Fatalf("events=%v err=%v", events, err)
			}
		})
	}
}

func TestBootstrapRejectsChangedOrUnusableExchange(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   capability.UnitKind
		change func(*controllerwire.ClaimedUnit)
	}{
		{"delivery", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Delivery = capability.DeliveryBootstrap }},
		{"empty_bearer", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Capability = "" }},
		{"bootstrap_bearer", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Capability = "one-time-bootstrap" }},
		{"team", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Request.Request.Team = "foreign" }},
		{"unit", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.UnitID = "foreign" }},
		{"run", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.RunID = "foreign" }},
		{"job", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.JobID = "foreign" }},
		{"holder", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Binding.Holder = "foreign" }},
		{"generation", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Binding.Generation++ }},
		{"attempt", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Binding.Attempt++ }},
		{"purpose", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Purpose = capability.PurposeHere }},
		{"lease", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.LeaseExpiresAt = time.Time{} }},
		{"owner_deadline", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.OwnerDeadline = nil }},
		{"changed_owner_deadline", capability.UnitNode, func(u *controllerwire.ClaimedUnit) {
			value := u.OwnerDeadline.Add(time.Second)
			u.OwnerDeadline = &value
		}},
		{"attempt_deadline", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.AttemptDeadline = nil }},
		{"parent_expiry", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.ParentCapabilityExpiry = nil }},
		{"engine", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Engine = nil }},
		{"accepted", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Accepted = nil }},
		{"accepted_engine", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Accepted.Spec.Resolved.Engine.Version = "v9.9.9" }},
		{"replacement_engine", capability.UnitNode, func(u *controllerwire.ClaimedUnit) {
			value := *u.Engine
			value.Version = "v9.9.9"
			u.Engine, u.Accepted.Spec.Resolved.Engine = &value, value
		}},
		{"resources", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Execution = nil }},
		{"cpu", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Execution.Resources.Value.Cores.Value++ }},
		{"memory", capability.UnitNode, func(u *controllerwire.ClaimedUnit) { u.Execution.Resources.Value.MemoryBytes.Value++ }},
		{"eval_lease", capability.UnitEval, func(u *controllerwire.ClaimedUnit) { u.Binding.Expansion.Generation++ }},
		{"eval_source", capability.UnitEval, func(u *controllerwire.ClaimedUnit) { u.SourceGeneration++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			award, unit := bootstrapAward(t, test.kind)
			test.change(&unit)
			calls := 0
			host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
				return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
					calls++
					return controllerwire.ExchangeResponse{Claim: unit}, nil
				}}, nil
			}, Host: bootstrapUnitHost(func(context.Context, controllerwire.ClaimedUnit) error {
				t.Fatal("invalid exchange reached execution")
				return nil
			})}
			if err := host.Run(t.Context(), award); err == nil || calls != 1 || strings.Contains(err.Error(), unit.Capability) && unit.Capability != "" {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBootstrapTransportFailureDoesNotReplayOrExecute(t *testing.T) {
	for _, phase := range []string{"connect", "missing_client", "exchange", "cancel_before", "cancel_after"} {
		t.Run(phase, func(t *testing.T) {
			award, unit := bootstrapAward(t, capability.UnitNode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure, connects, exchanges := errors.New("transport lost"), 0, 0
			host := BootstrapHost{ControllerURL: "https://controller.example", Host: bootstrapUnitHost(func(context.Context, controllerwire.ClaimedUnit) error {
				t.Fatal("failed exchange executed")
				return nil
			})}
			host.NewController = func(client.EngineOptions) (controllerwire.Controller, error) {
				connects++
				if phase == "connect" {
					return nil, failure
				}
				if phase == "missing_client" {
					return nil, nil
				}
				return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
					exchanges++
					if phase == "exchange" {
						return controllerwire.ExchangeResponse{}, failure
					}
					cancel()
					return controllerwire.ExchangeResponse{Claim: unit}, nil
				}}, nil
			}
			if phase == "cancel_before" {
				cancel()
			}
			if err := host.Run(ctx, award); err == nil || connects > 1 || exchanges > 1 || phase == "cancel_before" && connects != 0 {
				t.Fatalf("connects=%d exchanges=%d err=%v", connects, exchanges, err)
			}
		})
	}
}

func FuzzBootstrapBinding(f *testing.F) {
	f.Add("foreign-value", uint8(0))
	f.Add("private-unit", uint8(3))
	f.Add("", uint8(6))
	f.Fuzz(func(t *testing.T, foreign string, field uint8) {
		award, unit := bootstrapAward(t, capability.UnitNode)
		expected := ""
		switch field % 7 {
		case 0:
			expected, unit.UnitID = unit.UnitID, foreign
		case 1:
			expected, unit.RunID = unit.RunID, foreign
			unit.Request.RunID, unit.Binding.RunID = foreign, foreign
		case 2:
			expected, unit.JobID = unit.JobID, foreign
			unit.Binding.NodeID = foreign
		case 3:
			expected, unit.Request.Request.Team = unit.Request.Request.Team, foreign
		case 4:
			expected, unit.Binding.Holder = unit.Binding.Holder, foreign
		case 5:
			expected, unit.Capability = unit.Capability, foreign
		case 6:
			unit.Binding.Generation += int64(len(foreign)) + 1
		}
		if field%7 != 6 && foreign == expected {
			return
		}
		called := false
		host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
				return controllerwire.ExchangeResponse{Claim: unit}, nil
			}}, nil
		}, Host: bootstrapUnitHost(func(context.Context, controllerwire.ClaimedUnit) error { called = true; return nil })}
		err := host.Run(t.Context(), award)
		valid := field%7 == 5 && foreign != "" && foreign != string(award.Bootstrap)
		if called != valid || (err == nil) != valid {
			t.Fatal("exchange crossed its original binding or dispatched unusable authority")
		}
	})
}
