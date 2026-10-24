package runners

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	award "github.com/sparkwing-dev/sparkwing/internal/runners/award"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type sourceRefusalExecutor struct{ t *testing.T }

func (e sourceRefusalExecutor) Execute(context.Context, nodehost.NodeInput) error {
	e.t.Fatal("sourceless award reached execution")
	return nil
}

func TestClaimedRunnerRefusesAwardWithoutSourceBeforeAcquisition(t *testing.T) {
	unit, principal, state := hostingStateFor(t, capability.UnitNode)
	input, err := nodehost.NewRunInput(unit)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := NativeInputs(t.Context(), state, unit, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	host := nodehost.ClaimedNodeHost{
		Store: state, Executor: sourceRefusalExecutor{t}, Masker: secrets.NewMasker(),
		Acquire: func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
			t.Fatal("sourceless award reached acquisition")
			return nil, nil
		},
	}
	host.Lease.Interval, host.Lease.Lease = time.Second, time.Minute
	if err := host.Execute(t.Context(), inputs.Node); err == nil || !strings.Contains(err.Error(), "sealed source requires exactly one acquisition location") {
		t.Fatalf("sourceless award: %v", err)
	}
}

type hostingState struct {
	nodehost.NativeHostStore
	snapshot store.RunSnapshot
	node     runstate.NodeRow
}

func (s *hostingState) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return s.snapshot, nil
}

func (s *hostingState) ReadNode(context.Context, runspec.NodeRef) (runstate.NodeRow, error) {
	return s.node, nil
}

func hostingStateFor(t *testing.T, kind capability.UnitKind) (controllerwire.ClaimedUnit, capability.Principal, *hostingState) {
	t.Helper()
	unit := hostingAward(kind)
	if kind == capability.UnitNode {
		unit.Engine.Pin = engineid.Pin{SDK: engineid.GoSDK, Version: unit.Engine.Version}
		unit.Engine.ManifestDigest = "manifest"
		unit.Accepted.Spec.Resolved.Engine = *unit.Engine
		unit.Accepted.Spec.PlanHash = unit.Accepted.PlanHash
		unit.Runner.Labels = []string{}
	}
	unit.Request.Request.RequestedArgs = map[string]string{"arg": "sealed"}
	p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "award"}, Capability: capability.Capability{
		Prefix: "award", Issuance: capability.Award, Team: "team", Unit: kind, UnitID: unit.UnitID, Purpose: unit.Purpose, Delivery: unit.Delivery, Binding: unit.Binding,
	}}
	row := store.RunRow{Team: "team", Execution: runstate.Execution{ID: unit.RunID, Request: unit.Request, Owner: &runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(unit.RunID), Holder: unit.Binding.Holder, Generation: runspec.ClaimGeneration(unit.Binding.Generation)}}}}
	state := &hostingState{snapshot: store.RunSnapshot{Run: row}}
	if kind == capability.UnitNode {
		unit.Accepted.Spec.Request = unit.Request
		state.snapshot.Run.Spec = &unit.Accepted.Spec
		state.snapshot.Run.Plan = unit.Accepted.Plan
		state.snapshot.Run.PlanHash = string(unit.Accepted.PlanHash)
		state.node = runstate.NodeRow{Ref: runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)}, CapabilityPrefix: p.Capability.Prefix, ClaimedBy: unit.Binding.Holder, Generation: runspec.ClaimGeneration(unit.Binding.Generation), Attempt: runspec.AttemptOrdinal(unit.Binding.Attempt), SpecHash: "job-hash", Status: runstate.StatusPending, Execution: *unit.Execution}
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
			input, err := nodehost.NewRunInput(unit)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NativeInputs(t.Context(), state, unit, p, input)
			if err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitNode {
				if got.Node.Entry != nil || got.Node.Authority.CapabilityPrefix != p.Capability.Prefix || got.Node.Engine.Identity != *unit.Engine || !reflect.DeepEqual(got.Node.GrantedRunner, unit.Runner) || got.Node.SpecHash != state.node.SpecHash || got.Node.Generation != runspec.ClaimGeneration(unit.Binding.Generation) || got.Node.Attempt != runspec.AttemptOrdinal(unit.Binding.Attempt) {
					t.Fatalf("node input=%+v", got.Node)
				}
			} else if got.Run.Owner.Authority != p.Authority || got.Run.Owner.Owner != *state.snapshot.Run.Owner {
				t.Fatalf("owner=%+v", got.Run.Owner)
			}
		})
	}
}

func TestAwardInputRejectsMalformedPlan(t *testing.T) {
	for _, body := range []string{`{"jobs":[],"unknown":true}`, `{"jobs":[]} {}`, `{"jobs":`, `{"jobs":"invalid"}`} {
		t.Run(body, func(t *testing.T) {
			unit, _, _ := hostingStateFor(t, capability.UnitNode)
			unit.Accepted.Plan = json.RawMessage(body)
			if _, err := nodehost.NewRunInput(unit); err == nil {
				t.Fatal("malformed acceptance reached host construction")
			}
		})
	}
}

func FuzzAwardInputCapturesRetrySeals(f *testing.F) {
	f.Add([]byte("original"), false)
	f.Add([]byte("retry"), true)
	f.Fuzz(func(t *testing.T, data []byte, node bool) {
		replacement := "changed-" + hex.EncodeToString(data)
		kind := capability.UnitTrigger
		if node {
			kind = capability.UnitNode
		}
		unit, principal, state := hostingStateFor(t, kind)
		unit.Request.RunID = "source-" + hex.EncodeToString(data)
		state.snapshot.Run.Request = unit.Request
		unit.Runner.Labels = []string{"granted"}
		if node {
			unit.Accepted.Spec.Request = unit.Request
			state.snapshot.Run.Spec = &unit.Accepted.Spec
		}
		if err := award.ValidateAward(unit, capability.DeliveryDirect); err != nil {
			t.Fatal(err)
		}
		input, err := nodehost.NewRunInput(unit)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NativeInputs(t.Context(), state, unit, principal, input); err != nil {
			t.Fatal(err)
		}
		if node && input.Accepted.RunID != unit.RunID {
			t.Fatal("accepted execution retained the source run ID")
		}
		var body runspec.Request
		if err := json.Unmarshal(input.SealedRequest, &body); err != nil || !reflect.DeepEqual(body, unit.Request.Request) || input.RunID != unit.RunID {
			t.Fatalf("input lost execution identity or sealed request: %v", err)
		}
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		unit.Request.Request.RequestedArgs["arg"] = replacement
		unit.Runner.Labels[0] = replacement
		if node {
			unit.Accepted.Spec.Request.Hash = runspec.Hash(replacement)
			unit.Accepted.Plan[0] = '['
		}
		after, err := json.Marshal(input)
		if err != nil || string(before) != string(after) || input.Owner.Authority != (capability.AuthorityRef{}) || input.Owner.CapabilityPrefix != "" {
			t.Fatalf("captured inputs changed or acquired authority: %v", err)
		}
	})
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
		input, err := nodehost.NewRunInput(unit)
		if err == nil {
			_, err = NativeInputs(t.Context(), state, unit, p, input)
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

func FuzzNativePrincipalKeepsClaimAcrossAttempts(f *testing.F) {
	for field := uint8(0); field < 9; field++ {
		f.Add(uint32(1), field)
	}
	f.Fuzz(func(t *testing.T, ordinal uint32, field uint8) {
		unit, p, _ := hostingStateFor(t, capability.UnitNode)
		unit.Binding.Attempt = 0
		p.Capability.Binding = unit.Binding
		p.Capability.Binding.Attempt = int64(ordinal) + 1
		switch field % 9 {
		case 1:
			p.Capability.Binding.Generation++
		case 2:
			p.Capability.Binding.Holder += "-foreign"
		case 3:
			p.Capability.Binding.RunID += "-foreign"
		case 4:
			p.Capability.Binding.NodeID += "-foreign"
		case 5:
			p.Capability.Prefix += "-foreign"
		case 6:
			p.Capability.UnitID += "-foreign"
		case 7:
			unit.Binding.Attempt = p.Capability.Binding.Attempt + 1
		case 8:
			p.Capability.Binding.Attempt = -1
		}
		if principalMatches(unit, p) != (field%9 == 0) {
			t.Fatal("claim refresh confused an execution attempt with its immutable award fence")
		}
	})
}
