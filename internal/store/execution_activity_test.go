package store

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestExecutionActivityKeepsTenantAndStoreClockProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(471, 896))
	for range 24 {
		s, demand := unitClaimFixture(t)
		now := s.now()
		s.clock = func() time.Time { return now }
		claim, err := s.ClaimUnit(t.Context(), demand)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE team='a'`, now.Add(time.Duration(random.Int64N(int64(time.Hour)))+1).UnixNano())
		foreign := &Tenant{s: s, team: "b"}
		got, err := foreign.ExecutionActivity(t.Context())
		if err != nil || got != (ExecutionActivity{}) {
			t.Fatalf("foreign activity=%+v: %v", got, err)
		}
		owner := &Tenant{s: s, team: claim.Team}
		for i, at := range []time.Time{now, now.Add(time.Hour)} {
			now = at
			got, err := owner.ExecutionActivity(t.Context())
			held := 0
			if i == 0 {
				held = 1
			}
			if err != nil || got != (ExecutionActivity{Runs: 1, QueuedTriggers: 1, HeldLeases: held}) {
				t.Fatalf("activity=%+v: %v", got, err)
			}
		}
	}
}
