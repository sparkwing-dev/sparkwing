package store

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAuthenticationCancellationIntentPrecedenceProperty(t *testing.T) {
	tenant, _, actor := liveHolderFixture(t)
	random := rand.New(rand.NewPCG(582, 71))
	for range 64 {
		runReason := fmt.Sprintf("run-%x", random.Uint64())
		jobReason := fmt.Sprintf("job-%x", random.Uint64())
		if random.IntN(2) == 0 {
			jobReason = ""
		}
		jobCancelled := random.IntN(2) == 0
		var jobAt any
		want := runReason
		if jobCancelled {
			jobAt, want = 1, jobReason
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1,cancel_reason=? WHERE team='a' AND id='run'`, runReason)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET cancel_requested_at=?,cancel_reason=? WHERE team='a' AND run_id='run' AND node_id='job'`, jobAt, jobReason)
		snapshot, err := tenant.s.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256([]byte(actor.CapabilityPrefix)), Audience: capability.Controller}, capability.RequestEvidence{})
		if err != nil || !snapshot.Binding.RunCancelled || snapshot.Binding.CancelReason != want || snapshot.Binding.FinishedAt != nil {
			t.Fatalf("cancellation snapshot: %+v, %v", snapshot.Binding, err)
		}
	}
}
