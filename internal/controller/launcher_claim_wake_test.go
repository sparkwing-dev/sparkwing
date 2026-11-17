package controller

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type launcherMissedWake struct{ ClaimWake }

func (w launcherMissedWake) NextCommit() <-chan struct{} { return make(chan struct{}) }

func TestCloudLauncherClaimWakesOnCommitAndRecoversMissedSignal(t *testing.T) {
	for _, readyAfter := range []time.Duration{time.Millisecond, time.Second} {
		t.Run(readyAfter.String(), func(t *testing.T) {
			for _, signal := range []bool{true, false} {
				t.Run(map[bool]string{true: "commit", false: "missed commit"}[signal], func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						f := newClaimHoldFixture(t, nil)
						f.raw = engineHTTPRoot(t, f.e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
						engineSQL(t, f.e.Store, `UPDATE runs SET dispatch='controller'`)
						tenant, err := f.e.Store.ForTeam(t.Context(), "a")
						if err != nil {
							t.Fatal(err)
						}
						if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "wake", CreatedBy: "fixture"}); err != nil {
							t.Fatal(err)
						}
						if !signal {
							f.e.launcherWake = launcherMissedWake{ClaimWake: f.e.Store}
						}
						const poll = 5 * time.Second
						done := f.claim(t.Context(), "/api/v1/launcher/claim", poll)
						synctest.Wait()
						select {
						case w := <-done:
							t.Fatalf("empty claim did not hold: %d %s", w.Code, w.Body.String())
						default:
						}
						time.Sleep(readyAfter)
						started := time.Now()
						if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: engineActor(f.owner)}); err != nil {
							t.Fatal(err)
						}
						if !signal {
							time.Sleep(poll - readyAfter)
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
							want = poll - readyAfter
						}
						elapsed := time.Since(started)
						t.Logf("real readiness commit to claim=%s", elapsed)
						if signal {
							expected := max(250*time.Millisecond-readyAfter, 0)
							if elapsed != expected {
								t.Fatalf("ready to claim=%s, want %s", elapsed, expected)
							}
						}
						if !signal && elapsed != want {
							t.Fatalf("missed signal recovery=%s, want %s", elapsed, want)
						}
					})
				})
			}
		})
	}
}

type launcherSearchProbe struct {
	ClaimWake
	searches []time.Time
}

func (p *launcherSearchProbe) NextCommit() <-chan struct{} {
	p.searches = append(p.searches, time.Now())
	return p.ClaimWake.NextCommit()
}

func TestCloudLauncherHeldSearchCoalescesCommits(t *testing.T) {
	for _, commits := range []int{0, 49} {
		t.Run(map[bool]string{true: "commit_every_100ms", false: "idle"}[commits > 0], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newClaimHoldFixture(t, nil)
				f.raw = engineHTTPRoot(t, f.e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
				probe := &launcherSearchProbe{ClaimWake: f.e.Store}
				f.e.launcherWake = probe
				start := time.Now()
				done := f.claim(t.Context(), "/api/v1/launcher/claim", 5*time.Second)
				synctest.Wait()
				for range commits {
					time.Sleep(100 * time.Millisecond)
					if err := f.e.Store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "root", Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
				}
				if unit := claimedJob(t, <-done); unit != nil {
					t.Fatal("unrelated commits awarded work")
				}
				t.Logf("commits=%d searches=%d held=%s", commits, len(probe.searches), time.Since(start))
				if time.Since(start) != 5*time.Second {
					t.Fatal("commit throttle changed hold deadline")
				}
				if commits == 0 && len(probe.searches) != 1 {
					t.Fatalf("idle searches=%d", len(probe.searches))
				}
				if commits > 0 && (len(probe.searches) < 2 || len(probe.searches) > 20) {
					t.Fatalf("49 commits caused %d searches, want 2..20", len(probe.searches))
				}
				for i := 1; i < len(probe.searches); i++ {
					if gap := probe.searches[i].Sub(probe.searches[i-1]); gap < 250*time.Millisecond {
						t.Fatalf("commit searches separated by %s", gap)
					}
				}
			})
		})
	}
}

func TestCloudLauncherCommitThrottleHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newClaimHoldFixture(t, nil)
		f.raw = engineHTTPRoot(t, f.e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
		probe := &launcherSearchProbe{ClaimWake: f.e.Store}
		f.e.launcherWake = probe
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := f.claim(ctx, "/api/v1/launcher/claim", 5*time.Second)
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		if err := f.e.Store.AsOperator().RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "root", Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		cancel()
		<-done
		if len(probe.searches) != 1 {
			t.Fatalf("commit bypassed the throttle before cancellation: %d searches", len(probe.searches))
		}
	})
}
