package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestConcurrencyFirstArrivalLocksEntryBeforeHolders(t *testing.T) {
	s, recorder := newRecordingStore(t)
	tenant, request, _ := planFixtureWithInput(t, runspec.DispatchController, nil, s)
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Concurrency: []nodeproto.ConcurrencySpec{{Group: "new", Scope: new("global"), Capacity: new(1)}}}}}
	var err error
	request.Seal.Plan, err = json.Marshal(testBodyPlan(doc))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Declarations: request.Seal.Declarations, Request: request.Seal.Request, Plan: doc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	actor := controllerJobAward(t, tenant, request.RunID)
	recorder.reset()
	result, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: request.RunID, NodeID: "job", Generation: int64(actor.Generation)}, actor)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("MayStart: %+v %v", result, err)
	}
	created, locked, holders := -1, -1, -1
	for i, statement := range recorder.snapshot() {
		switch {
		case strings.HasPrefix(statement, "INSERT INTO concurrency_entries"):
			if created < 0 {
				created = i
			}
		case created >= 0 && locked < 0 && strings.HasPrefix(statement, "SELECT capacity FROM concurrency_entries"):
			locked = i
		case holders < 0 && strings.HasPrefix(statement, "SELECT "+holderColumns) && strings.Contains(statement, "FROM concurrency_holders"):
			holders = i
		}
	}
	if created < 0 || locked <= created || holders <= locked {
		t.Fatalf("first-arrival lock order: created=%d locked=%d holders=%d", created, locked, holders)
	}
}

func TestAuthorityAndUnitAcquisitionsFollowCanonicalLockOrder(t *testing.T) {
	s, recorder := newRecordingStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO team_counters(team) VALUES ('a')`)
	s, _ = unitClaimFixtureStore(t, s)
	member := authorityMember(t, s, "member", RoleOwner)
	parent := authorityRoot("parent", "a")
	parent.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
	authorityPersist(t, s, parent)
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET capability_prefix=? WHERE hash=?`, parent.Prefix, member.Authority.ID)
	refs := []capability.AuthorityRef{member.Authority}
	depths := map[capability.AuthorityRef]int{{Kind: capability.AuthorityCapability, ID: parent.Prefix}: 0, member.Authority: 1}
	for _, name := range []string{"a", "z"} {
		child := authorityRoot("child-"+name, "a")
		child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}
		authorityPersist(t, s, child)
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
		refs = append(refs, ref)
		depths[ref] = 1
	}
	depths[capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}] = 0
	rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
	units := []UnitRef{{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, {Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 7}, {Kind: capability.UnitTrigger, RunID: "run"}}
	random := rand.New(rand.NewPCG(4901, 17))
	for range 32 {
		random.Shuffle(len(refs), func(i, j int) { refs[i], refs[j] = refs[j], refs[i] })
		random.Shuffle(len(units), func(i, j int) { units[i], units[j] = units[j], units[i] })
		recorder.reset()
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			if err := tx.operator.lockAuthorityTx(t.Context(), tx, refs); err != nil {
				return err
			}
			return tx.operator.lockUnitsTx(t.Context(), tx, units)
		})
		if err != nil {
			t.Fatal(err)
		}
		var acquired []storeLockKey
		for _, call := range recorder.snapshotCalls() {
			switch {
			case call.statement == "SELECT account_id FROM memberships WHERE team=? AND account_id=?":
				acquired = append(acquired, storeLockKey{tier: lockMembership, keys: [3]string{fmt.Sprint(call.args[0].Value), fmt.Sprint(call.args[1].Value)}})
			case call.statement == "SELECT prefix FROM capabilities WHERE prefix=?", call.statement == "SELECT hash FROM sessions WHERE hash=?":
				kind := capability.AuthorityCapability
				if strings.Contains(call.statement, "sessions") {
					kind = capability.AuthoritySession
				}
				ref := capability.AuthorityRef{Kind: kind, ID: fmt.Sprint(call.args[0].Value)}
				acquired = append(acquired, authorityLockKey(authorityLock{ref: ref, depth: depths[ref]}))
			case strings.HasPrefix(call.statement, "SELECT id FROM triggers WHERE team=? AND id=?"):
				acquired = append(acquired, storeLockKey{tier: lockTrigger})
			case strings.HasPrefix(call.statement, "SELECT run_id FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?"):
				acquired = append(acquired, storeLockKey{tier: lockEval})
			case strings.HasPrefix(call.statement, "SELECT id FROM runs WHERE team=? AND id=?"):
				acquired = append(acquired, storeLockKey{tier: lockRun})
			case strings.HasPrefix(call.statement, "SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?"):
				acquired = append(acquired, storeLockKey{tier: lockNode})
			}
		}
		if len(acquired) != len(depths)+5 || !slices.IsSortedFunc(acquired, compareStoreLocks) {
			t.Fatalf("acquisitions violate canonical order: %+v", acquired)
		}
	}
}
