package store

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestBindingDataUsesTransactionalPersistence(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	target := capability.Target{Team: "a", RunID: "run", Data: &capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}}
	for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
		if err := s.AsOperator().CheckBinding(t.Context(), p, rule, target); err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("keep receipt")
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			rewriteSchemaExec(t, tx, "DELETE FROM blob_receipts WHERE team='a' AND digest=?", intent.Digest)
			if err := tx.CheckBinding(t.Context(), p, rule, target); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("%s ignored receipt deletion: %v", rule, err)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		if err := s.AsOperator().CheckBinding(t.Context(), p, rule, target); err != nil {
			t.Fatalf("binding check committed receipt deletion: %v", err)
		}
	}
}

func TestBindingDataCannotWidenPersistedAuthority(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	property := func(seed uint8, forged string) bool {
		input := capability.Principal{Authority: p.Authority, Capability: authorityRoot(forged, "forged")}
		input.Capability.Grants = []capability.Grant{capability.TeamAdmin, capability.DataRead, capability.DataWrite}
		target := capability.Target{Team: "a", RunID: "run", Data: &capability.DataTarget{Operation: capability.DataDownload, Digest: string(intent.Digest)}}
		for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
			if err := s.AsOperator().CheckBinding(t.Context(), input, rule, target); err != nil {
				t.Logf("persisted authority depended on supplied fields: %v", err)
				return false
			}
		}
		rollback := errors.New("restore authority")
		err := s.inTx(t.Context(), func(tx *storeTx) error {
			switch seed % 6 {
			case 0:
				target.Team = "b"
			case 1:
				target.Data.Digest = strings.Repeat("e", 64)
			case 2:
				rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", p.Authority.ID)
			case 3:
				rewriteSchemaExec(t, tx, "UPDATE capabilities SET revoked_at=1 WHERE prefix='machine'")
			case 4:
				rewriteSchemaExec(t, tx, "UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'")
			case 5:
				rewriteSchemaExec(t, tx, "UPDATE nodes SET lease_expires_at=1 WHERE team='a' AND run_id='run' AND node_id='job'")
			}
			for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
				if err := tx.CheckBinding(t.Context(), input, rule, target); err == nil {
					return errors.New("forged authority bypassed persisted data boundary")
				}
			}
			return rollback
		})
		return errors.Is(err, rollback)
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(5109))})); err != nil {
		t.Fatal(err)
	}
}
