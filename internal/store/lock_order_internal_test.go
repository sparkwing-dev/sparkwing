package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

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
			if err := lockAuthorityTx(t.Context(), tx, refs); err != nil {
				return err
			}
			return lockUnitsTx(t.Context(), tx, units)
		})
		if err != nil {
			t.Fatal(err)
		}
		var acquired []storeLockKey
		for _, call := range recorder.snapshotCalls() {
			switch {
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
		if len(acquired) != len(depths)+4 || !slices.IsSortedFunc(acquired, compareStoreLocks) {
			t.Fatalf("acquisitions violate canonical order: %+v", acquired)
		}
	}
}
