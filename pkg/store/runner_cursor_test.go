package store

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"
)

func TestRunnerObservationCursorAndLivenessProperty(t *testing.T) {
	s, _ := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	authorityPersist(t, s, authorityRoot("runner", "a"))
	s.dialect = DialectPostgres
	random := rand.New(rand.NewPCG(43, 71))
	for sample := range 36 {
		now := time.Now().UTC()
		age := time.Duration(1+random.IntN(900)) * time.Second
		if sample%3 == 1 {
			age += 24 * time.Hour
		} else if sample%3 == 2 {
			age = -age
		}
		body, err := json.Marshal(runnerPoll{ObservedAt: now.Add(-age)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.exec(t.Context(), `UPDATE capabilities SET last_used_at=?,claim_observation_json=? WHERE prefix='runner'`, now.UnixNano(), body); err != nil {
			t.Fatal(err)
		}
		var placed []UnitDemand
		err = s.inTx(t.Context(), func(tx *storeTx) error {
			var err error
			placed, err = placementRunnersTx(t.Context(), tx, "a", ClaimPlacement{Liveness: time.Hour})
			return err
		})
		want := 0
		if sample%3 == 0 {
			want = 1
		}
		if err != nil || len(placed) != want {
			t.Fatalf("age=%s: placement observations=%d, want %d: %v", age, len(placed), want, err)
		}
		var inventory []RegisteredAgent
		err = s.inTx(t.Context(), func(tx *storeTx) error {
			var err error
			inventory, err = registeredAgentObservationsTx(t.Context(), tx, "a")
			return err
		})
		if sample%3 == 2 {
			if err == nil || len(inventory) != 0 {
				t.Fatalf("future observation accepted: %+v: %v", inventory, err)
			}
		} else if err != nil || len(inventory) != 1 || !inventory[0].poll.ObservedAt.Equal(now.Add(-age)) {
			t.Fatalf("age=%s: inventory observations=%d: %v", age, len(inventory), err)
		}
	}
}
