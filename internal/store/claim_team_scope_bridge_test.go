package store

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// AddIdleTeams creates n live teams that hold no work.
func (f *RankingFixture) AddIdleTeams(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		f.Store.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, fmt.Sprintf("idle-%02d", i))
	}
}

// SetPaymentMode configures the store's payment mode.
func (f *RankingFixture) SetPaymentMode(t *testing.T, mode capability.PaymentMode) {
	t.Helper()
	f.Store.testOnlyExec(t, `INSERT INTO settings(key,value) VALUES ('payment_mode',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, []byte(`"`+string(mode)+`"`))
}

// Fund grants team a free credits in the current payment mode.
func (f *RankingFixture) Fund(t *testing.T) { t.Helper(); fundUnitClaim(t, f.Store) }

// ClaimTeams lists, in fairness order, the teams whose claim facts an unscoped search builds. With kinds it is the
// Cloud launcher's list; without, every live team.
func (f *RankingFixture) ClaimTeams(t *testing.T, kinds ...capability.UnitKind) []string {
	t.Helper()
	tx, err := f.Store.beginSnapshotReadTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	facts, err := claimTeamFactsTx(t.Context(), tx, "", kinds)
	if err != nil {
		t.Fatal(err)
	}
	var teams []string
	for _, team := range facts {
		teams = append(teams, team.Team)
	}
	return teams
}

// ReadyJobAgain marks a requeued job of the fixture run ready at the current clock, as its plan's release does.
func (f *RankingFixture) ReadyJobAgain(t *testing.T, id string) {
	t.Helper()
	f.Store.testOnlyExec(t, `UPDATE nodes SET ready_at=?,queue_started_at=COALESCE(queue_started_at,?) WHERE team='a' AND run_id='run' AND node_id=?`, f.now.UnixNano(), f.now.UnixNano(), id)
}
