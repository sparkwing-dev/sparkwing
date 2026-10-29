package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestBlobDataBindingUsesTransactionalReadSet(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	principal := dataAccessAuthority(t, s, actor.CapabilityPrefix).Principal
	data := capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}
	target := capability.Target{Team: "a", RunID: "run", Data: &data}
	rollback := errors.New("rollback receipt removal")
	for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
		if err := s.AsOperator().CheckBinding(t.Context(), principal, rule, target); err != nil {
			t.Fatal(err)
		}
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			rewriteSchemaExec(t, tx, "DELETE FROM blob_receipts WHERE team='a' AND digest=?", intent.Digest)
			if err := tx.CheckBinding(t.Context(), principal, rule, target); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("binding ignored its transaction's receipt removal: %v", err)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("binding did not return control to its transaction owner: %v", err)
		}
		if err := s.AsOperator().CheckBinding(t.Context(), principal, rule, target); err != nil {
			t.Fatal(err)
		}
	}
	property := func(forged string) bool {
		supplied := principal
		supplied.Capability.Team = forged
		supplied.Capability.Prefix = forged
		supplied.Capability.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
		supplied.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
		for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
			if err := s.AsOperator().CheckBinding(t.Context(), supplied, rule, target); err != nil {
				return false
			}
			unknown := data
			unknown.Digest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			selected := target
			selected.Data = &unknown
			if err := s.AsOperator().CheckBinding(t.Context(), supplied, rule, selected); !errors.Is(err, blob.ErrDigestNotReadable) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(47))})); err != nil {
		t.Fatal(err)
	}
}
