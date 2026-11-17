package controller

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type launcherMissedWake struct{ ClaimWake }

func (w launcherMissedWake) NextCommit() <-chan struct{} { return make(chan struct{}) }

func TestCloudLauncherClaimWakesOnCommitAndRecoversMissedSignal(t *testing.T) {
	for _, signal := range []bool{true, false} {
		t.Run(map[bool]string{true: "commit", false: "missed commit"}[signal], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newClaimHoldFixture(t, nil)
				if f.e.launcherWake != f.e.Store {
					t.Fatal("Cloud launcher commit signal is not wired")
				}
				f.raw = engineHTTPRoot(t, f.e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
				engineSQL(t, f.e.Store, `UPDATE runs SET dispatch='controller'`)
				tenant, err := f.e.Store.ForTeam(t.Context(), "a")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "wake", CreatedBy: "fixture"}); err != nil {
					t.Fatal(err)
				}
				wake := ClaimWake(f.e.Store)
				if !signal {
					wake = launcherMissedWake{ClaimWake: wake}
				}
				f.e.launcherWake = wake
				const poll = 5 * time.Second
				done := f.claim(t.Context(), "/api/v1/launcher/claim", poll)
				synctest.Wait()
				select {
				case w := <-done:
					t.Fatalf("empty claim did not hold: %d %s", w.Code, w.Body.String())
				default:
				}
				time.Sleep(time.Millisecond)
				started := time.Now()
				engineSQL(t, f.e.Store, `UPDATE nodes SET ready_at=1,queue_started_at=? WHERE run_id='run' AND node_id='job'`, time.Now().UnixNano())
				if signal {
					if err := f.e.Store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "root", Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
						t.Fatal(err)
					}
				} else {
					time.Sleep(poll - time.Millisecond)
					if unit := claimedJob(t, <-done); unit != nil {
						t.Fatal("missed signal unexpectedly awarded before the rescan")
					}
					done = f.claim(t.Context(), "/api/v1/launcher/claim", poll)
				}
				unit := claimedJob(t, <-done)
				if unit == nil || unit.JobID != "job" || unit.Delivery != capability.DeliveryBootstrap || unit.Capability == "" {
					t.Fatalf("wake lost fenced bootstrap award: %+v", unit)
				}
				want := time.Duration(0)
				if !signal {
					want = poll - time.Millisecond
				}
				if elapsed := time.Since(started); elapsed != want {
					t.Fatalf("ready to claim=%s, want %s", elapsed, want)
				}
			})
		})
	}
}
