package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestBillingClockRestoreWarnsWhenFarAhead(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, ahead := range []time.Duration{0, time.Minute, time.Minute + time.Nanosecond} {
			t.Run(fmt.Sprintf("existing=%t/ahead=%s", existing, ahead), func(t *testing.T) {
				now := time.Unix(1000, 0).UTC()
				path := filepath.Join(t.TempDir(), "billing.db")
				s, err := Open(path, WithTestWallClock(func() time.Time { return now }))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(reference,id,team,kind,amount_micro,created_at) VALUES ('fund','fund','a','paid',1000000,1)`)
				reserveMetering(t, s, meteringAward("plan", "at_award"))
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				now = now.Add(-ahead)
				var logs bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previous) })
				if existing {
					s, err = OpenExisting(t.Context(), DialectSQLite, path, WithTestWallClock(func() time.Time { return now }))
				} else {
					s, err = Open(path, WithTestWallClock(func() time.Time { return now }))
				}
				if err != nil {
					t.Fatal(err)
				}
				warned := strings.Contains(logs.String(), "level=WARN") && strings.Contains(logs.String(), "restored billing clock is ahead of wall time")
				if warned != (ahead > time.Minute) {
					t.Fatalf("ahead=%s, logs=%s", ahead, logs.String())
				}
			})
		}
	}
}

// safety: matches WSL time sync, which steps the wall clock back about 3.3 seconds every half minute.
const backwardClockStep = 3300 * time.Millisecond

// safety: the fixture must use the production SQLite clock path, where the floor applies.
func steppingWallClock(s *Store, now *time.Time) {
	s.clock, s.pinnedClock = nil, nil
	s.clockFloor.Store(0)
	s.wallClock = func() time.Time { return *now }
}

func TestStepFinishSurvivesBackwardClockStep(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	now := s.now()
	steppingWallClock(s, &now)
	report := StepReport{Attempt: attempt, Actor: actor, StepID: "check", State: "running"}
	if err := tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-backwardClockStep)
	report.State = stepPassed
	if err := tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal("step finish after a backward clock step:", err)
	}
	var started, finished int64
	if err := s.db.QueryRow(`SELECT started_at,finished_at FROM node_steps WHERE team='a' AND run_id='run' AND node_id='job' AND step_id='check'`).Scan(&started, &finished); err != nil || finished < started {
		t.Fatalf("step finished before it started: started=%d finished=%d error=%v", started, finished, err)
	}
}

func TestPlanSettlesAfterBackwardClockStep(t *testing.T) {
	for _, completion := range []string{"accepted", "invalid"} {
		t.Run(completion, func(t *testing.T) {
			tenant, request, now := planFixture(t, runspec.DispatchController)
			steppingWallClock(tenant.s, now)
			awarded := now.Add(545 * time.Millisecond)
			*now = awarded
			if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
				owner, err := loadCapabilityTx(t.Context(), tx, request.Actor.CapabilityPrefix)
				if err != nil {
					return err
				}
				return recordBillingAwardTx(t.Context(), tx, owner, CreditRate{}, 0)
			}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(-backwardClockStep)
			if completion == "accepted" {
				if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
					t.Fatal("plan acceptance after a backward clock step:", err)
				}
			} else {
				_, err := tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{
					Unit:       request.Actor.Owner,
					Transition: runstate.RunCompletion{RunID: runspec.RunID(request.RunID), Actor: request.Actor, Cause: runstate.RunFinish, MaskedError: "invalid plan"},
				})
				if err != nil {
					t.Fatal("invalid-plan completion after a backward clock step:", err)
				}
			}
			var settled int64
			if err := tenant.s.db.QueryRow(`SELECT settled_at FROM billing_awards WHERE capability_prefix=?`, request.Actor.CapabilityPrefix).Scan(&settled); err != nil || settled < awarded.UnixNano() {
				t.Fatalf("settlement moved before its award: settled=%d awarded=%d error=%v", settled, awarded.UnixNano(), err)
			}
		})
	}
}

func TestMeteredCheckpointSurvivesBackwardClockReopen(t *testing.T) {
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			paused := state == "paused"
			began := time.Unix(1000, 0).UTC()
			now := began
			path := filepath.Join(t.TempDir(), "billing.db")
			s, err := Open(path, WithTestWallClock(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(reference,id,team,kind,amount_micro,created_at) VALUES ('fund','fund','a','paid',1000000,1)`)
			reserveMetering(t, s, meteringAward("plan", "at_award"))
			now = began.Add(75 * time.Second)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", began.Add(70*time.Second), false)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if paused {
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "award", began.Add(75*time.Second))
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path, WithTestWallClock(func() time.Time { return began.Add(-backwardClockStep) }))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for replay := range 2 {
				if err := reopened.inTx(t.Context(), func(tx *storeTx) error {
					result, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", tx.now(t.Context()), true)
					want := int64(70)
					if paused {
						want = 75
					}
					if err == nil && (result.BillableSeconds != want || replay == 0 && result.ElapsedSeconds != want) {
						t.Fatalf("settlement=%+v, want %d seconds", result, want)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var cost int64
			if err := reopened.db.QueryRow(`SELECT SUM(amount_micro) FROM credit_charges WHERE team='a'`).Scan(&cost); err != nil {
				t.Fatal(err)
			}
			wantCost := int64(700)
			if paused {
				wantCost = 750
			}
			if cost != wantCost {
				t.Fatalf("cost=%d, want %d", cost, wantCost)
			}
		})
	}
}

func TestLogUploadClockSurvivesBackwardStep(t *testing.T) {
	for _, step := range []time.Duration{time.Millisecond, backwardClockStep} {
		t.Run(step.String(), func(t *testing.T) {
			s, _, _, _ := resultFixture(t)
			now := s.now()
			steppingWallClock(s, &now)
			first, err := s.LogUploadNow(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(-step)
			after, err := s.LogUploadNow(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !after.Equal(first) {
				t.Fatalf("clock moved backward: %s -> %s", first, after)
			}
		})
	}
}
