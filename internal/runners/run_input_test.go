package runners

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type hostingState struct {
	nodehost.NativeHostStore
	snapshot store.RunSnapshot
	node     store.NodeRow
}

func (s *hostingState) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return s.snapshot, nil
}

func (s *hostingState) ReadNode(context.Context, store.NodeRef) (store.NodeRow, error) {
	return s.node, nil
}

func hostingStateFor(t *testing.T, kind capability.UnitKind) (controllerwire.ClaimedUnit, capability.Principal, *hostingState) {
	t.Helper()
	unit := hostingAward(kind)
	if kind == capability.UnitNode {
		unit.Engine.Pin = engine.Pin{SDK: engine.GoSDK, Version: unit.Engine.Version}
		unit.Engine.ManifestDigest = "manifest"
		unit.Accepted.Spec.Resolved.Engine = *unit.Engine
		unit.Accepted.Spec.PlanHash = unit.Accepted.PlanHash
		unit.Runner.Labels = []string{}
	}
	unit.Request.Request.RequestedArgs = map[string]string{"arg": "sealed"}
	p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "award"}, Capability: capability.Capability{
		Prefix: "award", Issuance: capability.Award, Team: "team", Unit: kind, UnitID: unit.UnitID, Purpose: unit.Purpose, Delivery: unit.Delivery, Binding: unit.Binding,
	}}
	row := store.RunRow{ID: unit.RunID, Team: "team", Request: unit.Request, Owner: &store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: unit.RunID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation}}
	state := &hostingState{snapshot: store.RunSnapshot{Run: row}}
	if kind == capability.UnitNode {
		unit.Accepted.Spec.Request = unit.Request
		state.snapshot.Run.Spec = &unit.Accepted.Spec
		state.snapshot.Run.Plan = unit.Accepted.Plan
		state.snapshot.Run.PlanHash = string(unit.Accepted.PlanHash)
		state.node = store.NodeRow{Ref: store.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}, CapabilityPrefix: p.Capability.Prefix, ClaimedBy: unit.Binding.Holder, Generation: unit.Binding.Generation, Attempt: unit.Binding.Attempt, SpecHash: "job-hash", Status: store.StatusPending, Execution: *unit.Execution}
	}
	// safety: the store's snapshot must not share mutable objects with the received award.
	body, err := json.Marshal(state.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state.snapshot = store.RunSnapshot{}
	if err := json.Unmarshal(body, &state.snapshot); err != nil {
		t.Fatal(err)
	}
	return unit, p, state
}

func TestNativeInputsUsePersistedAuthority(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			unit, p, state := hostingStateFor(t, kind)
			input, err := runInputFor(unit)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nativeInputs(t.Context(), state, unit, p, input)
			if err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitNode {
				if got.node.Entry != nil || got.node.Authority.CapabilityPrefix != p.Capability.Prefix || got.node.Engine.Identity != *unit.Engine || !reflect.DeepEqual(got.node.GrantedRunner, unit.Runner) || got.node.SpecHash != state.node.SpecHash || got.node.Generation != unit.Binding.Generation || got.node.Attempt != unit.Binding.Attempt {
					t.Fatalf("node input=%+v", got.node)
				}
			} else if got.run.Owner.Authority != p.Authority || got.run.Owner.Owner != *state.snapshot.Run.Owner {
				t.Fatalf("owner=%+v", got.run.Owner)
			}
		})
	}
}

func FuzzNativeInputsRejectChangedAwards(f *testing.F) {
	for field := uint8(0); field < 10; field++ {
		f.Add(field, "foreign", int64(5))
	}
	f.Fuzz(func(t *testing.T, field uint8, value string, delta int64) {
		if value == "" {
			value = "foreign"
		}
		unit, p, state := hostingStateFor(t, capability.UnitNode)
		switch field % 10 {
		case 0:
			unit.Binding.Holder += value
		case 1:
			unit.Binding.Generation ^= delta | 1
		case 2:
			unit.Binding.Attempt ^= delta | 1
		case 3:
			unit.Request.Hash += runspec.Hash(value)
		case 4:
			unit.Request.Request.RequestedArgs["arg"] += value
		case 5:
			unit.Accepted.Spec.Hash += runspec.Hash(value)
		case 6:
			unit.Accepted.PlanHash += runspec.Hash(value)
		case 7:
			unit.Execution.Retry.Value++
		case 8:
			unit.Accepted.Spec.Resolved.Engine.Version += value
		case 9:
			unit.JobID += value
		}
		input, err := runInputFor(unit)
		if err == nil {
			_, err = nativeInputs(t.Context(), state, unit, p, input)
		}
		if err == nil {
			t.Fatal("changed award crossed the persisted execution fence")
		}
	})
}

func FuzzNativePrincipalBindingFence(f *testing.F) {
	f.Add(int64(2000000000), int32(-7*3600), false)
	f.Add(int64(1), int32(0), true)
	f.Fuzz(func(t *testing.T, seconds int64, offset int32, changed bool) {
		seconds = seconds % 4000000000
		unit, p, _ := hostingStateFor(t, capability.UnitTrigger)
		instant := time.Unix(seconds, 0).In(time.FixedZone("local", int(offset%841)*60))
		unit.Binding.ClaimBy, unit.Binding.Deadline = &instant, &instant
		p.Capability.Binding = unit.Binding
		body, err := json.Marshal(unit)
		if err != nil {
			t.Fatal(err)
		}
		var captured controllerwire.ClaimedUnit
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		if changed {
			value := instant.Add(time.Nanosecond)
			captured.Binding.Deadline = &value
		}
		if principalMatches(captured, p) == changed {
			t.Fatal("binding equality changed a claim fence or its deadline")
		}
	})
}
