package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRestoreHoldsSchedulesUntilAcknowledgement(t *testing.T) {
	s := authorityStore(t)
	s.credentialEpoch = "original"
	ctx := t.Context()
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(time.Hour)
	s.clock = func() time.Time { return now }
	for _, team := range []Team{"a", "b"} {
		sched := CronSchedule{ID: "nightly", RepoPath: "https://example.com/acme/repo", Pipeline: "nightly", Cron: "* * * * *", CatchUp: 24 * time.Hour}
		tenant := &Tenant{s: s, team: team}
		if _, _, err := tenant.ArmCronSchedule(ctx, sched); err != nil {
			t.Fatal(err)
		}
	}
	// safety: a lost or replaced epoch file is refused, not taken as a restore: nothing is held or cancelled.
	s.credentialEpoch = "rotated"
	if err := s.EnsureCredentialEpoch(ctx); !errors.Is(err, ErrCredentialEpochChanged) {
		t.Fatalf("changed epoch served: %v", err)
	}
	op := s.AsOperator()
	if state, err := op.RestoreStatus(ctx); err != nil || state.Pending {
		t.Fatalf("refused epoch held schedules: %+v %v", state, err)
	}
	if err := op.PrepareRestore(ctx, "rotated"); err != nil {
		t.Fatal(err)
	}
	if ran, err := op.RunCronTick(ctx, func(context.Context) error {
		t.Fatal("a held deployment ticked")
		return nil
	}); ran || !errors.Is(err, ErrRestorePending) {
		t.Fatalf("held tick = %t, %v", ran, err)
	}
	now = now.Add(2 * time.Hour)
	boundary := now
	if err := op.AcknowledgeRestore(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []Team{"a", "b"} {
		sched, err := (&Tenant{s: s, team: team}).GetCronSchedule(ctx, "nightly")
		if err != nil || !sched.CursorAt.Equal(boundary) || sched.NextDueAt != nil {
			t.Fatalf("acknowledged schedule = %+v, %v", sched, err)
		}
	}
	// An evaluator that loaded a due instant before acknowledgement cannot submit it afterwards.
	err := s.inTx(ctx, func(tx *storeTx) error { return checkRestoreScheduleTx(ctx, tx, base.Add(time.Minute)) })
	if !errors.Is(err, ErrRestorePending) {
		t.Fatalf("stale due admission = %v", err)
	}
	err = s.inTx(ctx, func(tx *storeTx) error { return checkRestoreScheduleTx(ctx, tx, boundary.Add(time.Minute)) })
	if err != nil {
		t.Fatalf("future due refused: %v", err)
	}
	now = now.Add(time.Hour)
	if err := op.AcknowledgeRestore(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := op.RestoreStatus(ctx)
	if err != nil || state.Pending || state.AcknowledgedAt != boundary.UnixNano() {
		t.Fatalf("repeated acknowledgement moved boundary = %+v, %v", state, err)
	}
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = op.RestoreStatus(ctx)
	if err != nil || state.Pending {
		t.Fatalf("same epoch reentered restore = %+v, %v", state, err)
	}
}

func TestRestorePreparationCancelsScheduledWorkBeforeClaims(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitNode} {
		for _, restored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restored=%t", kind, restored), func(t *testing.T) {
				var s *Store
				var tenant *Tenant
				var demand UnitDemand
				if kind == capability.UnitNode {
					s, demand = unitClaimFixture(t)
					fundUnitClaim(t, s)
					tenant = &Tenant{s: s, team: "a"}
				} else {
					s, tenant, demand, _ = cancellationFixture(t, kind)
				}
				rewriteSchemaExec(t, s.db, `UPDATE runs SET trigger_source='schedule'`)
				if restored {
					if err := s.AsOperator().PrepareRestore(t.Context(), "rotated"); err != nil {
						t.Fatal(err)
					}
					run, err := tenant.ReadRun(t.Context(), "run")
					if err != nil || run.CancelRequestedAt == nil || run.CancelReason != "cancelled by restore; rerun if needed" {
						t.Fatalf("restored run cancellation: at=%v reason=%q err=%v", run.CancelRequestedAt, run.CancelReason, err)
					}
				}
				claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				if restored && err == nil {
					t.Fatal("restored scheduled work was claimed")
				}
				if !restored && err != nil {
					t.Fatalf("backup work was not claimable: %v", err)
				}
				if !restored {
					if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

// Local source facts were captured when the checkout schedule was armed.
type restoreSchedulePolicy struct{ scheduleFixturePolicy }

func (p restoreSchedulePolicy) ResolveScheduleSource(ctx context.Context, row CronSchedule, request runspec.Request) (runspec.Request, error) {
	if row.Where == CronWhereLocal {
		return request, nil
	}
	return p.scheduleFixturePolicy.ResolveScheduleSource(ctx, row, request)
}

func TestRestorePreservesScheduleConsentUnlessEpochRotates(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, signOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("local=%t/sign-out=%t", local, signOut), func(t *testing.T) {
				tenant, a, body, now := consentedScheduleFixture(t)
				s := tenant.s
				s.credentialEpoch = "original"
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET credential_epoch='original'`)
				s.intakePolicy = restoreSchedulePolicy{s.intakePolicy.(scheduleFixturePolicy)}
				if err := s.EnsureCredentialEpoch(t.Context()); err != nil {
					t.Fatal(err)
				}
				var ids []string
				var err error
				if local {
					rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, DefaultTeam)
					tenant = &Tenant{s: s, team: DefaultTeam}
					snapshot, lookupErr := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}}, capability.RequestEvidence{})
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					principal := snapshot.Authorities[0].Principal
					a = capability.Authorized{Principal: principal, Target: capability.Target{Team: string(DefaultTeam)}}
					root := t.TempDir()
					repository, repoErr := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
					if repoErr != nil {
						t.Fatal(repoErr)
					}
					row := CronSchedule{RepoPath: root, Pipeline: "nightly", Cron: "* * * * *", Overlap: CronOverlapQueue, Where: CronWhereLocal, GitBranch: body.Branch, LockedRef: body.SHA}
					request := runspec.Request{Git: runspec.Git{Repository: repository, Ref: body.Branch, SHA: body.SHA}, Source: runspec.Source{Ref: body.SHA, Location: runspec.SourceLocation{LocalCheckout: root}}}
					ids, _, err = tenant.ReplaceAuthorizedLocalSchedules(t.Context(), a, root, []CronSchedule{row}, []runspec.Request{request}, true)
				} else {
					ids, _, err = tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
				}
				if err != nil {
					t.Fatal(err)
				}
				row, err := tenant.GetCronSchedule(t.Context(), ids[0])
				if err != nil {
					t.Fatal(err)
				}
				*now = *row.NextDueAt
				if result, err := tenant.AdmitScheduledRun(t.Context(), row, *now); err != nil || result.RunID == "" {
					t.Fatalf("pre-restore consent: %+v %v", result, err)
				}
				epoch := "original"
				if signOut {
					epoch = "rotated"
				}
				if err := s.AsOperator().PrepareRestore(t.Context(), epoch); err != nil {
					t.Fatal(err)
				}
				// The controller reopens the store with the external epoch after preparation.
				s.credentialEpoch = epoch
				*now = now.Add(time.Minute)
				if _, err := tenant.AdmitScheduledRun(t.Context(), row, *now); err == nil || !signOut && !errors.Is(err, ErrRestorePending) {
					t.Fatalf("schedule escaped restore hold: %v", err)
				}
				if err := s.AsOperator().AcknowledgeRestore(t.Context()); err != nil {
					t.Fatal(err)
				}
				row, err = tenant.GetCronSchedule(t.Context(), ids[0])
				if err != nil {
					t.Fatal(err)
				}
				*now = now.Add(time.Minute)
				result, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
				if signOut {
					if err == nil {
						t.Fatal("epoch rotation retained schedule consent")
					}
				} else if err != nil || result.RunID == "" || result.Replayed {
					t.Fatalf("normal restore required new consent: %+v %v", result, err)
				}
			})
		}
	}
}
