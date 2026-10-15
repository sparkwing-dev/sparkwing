package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreUnitBillingStart checks immutable reservations and compute starts through reopening.
func CheckStoreUnitBillingStart(ctx context.Context, root string) error {
	for _, cloud := range []bool{false, true} {
		if err := checkStoreUnitBillingStart(ctx, filepath.Join(root, fmt.Sprintf("billing-%t.db", cloud)), cloud); err != nil {
			return fmt.Errorf("cloud=%t: %w", cloud, err)
		}
	}
	return nil
}

func checkStoreUnitBillingStart(ctx context.Context, path string, cloud bool) (err error) {
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET claimed_by=NULL,capability_prefix='',generation=0,lease_expires_at=NULL,execution_json=?`, []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":4}}}}`)); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund',?,'paid',1000000,1)`, store.DefaultTeam); err != nil {
		return err
	}
	table := store.CreditRateTable{{Cores: 1, MicroPerSecond: 2}, {Cores: 4, MicroPerSecond: 10}}
	if _, err := f.s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateTable: &table}); err != nil {
		return err
	}
	until := time.Now().UTC().Add(time.Hour)
	runner := capability.Capability{Digest: sha256.Sum256([]byte("billing-root")), Prefix: "billing-root", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: time.Now().UTC(), ExpiresAt: &until, RootMetered: true}
	if cloud {
		runner.Team, runner.Subject.Kind = "", capability.SubjectService
		runner.Grants = []capability.Grant{capability.ClaimsLaunch}
	}
	if err := seedRecoveryCapability(ctx, f.s, runner); err != nil {
		return err
	}
	claim, err := f.s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: runner.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Available: match.Resources{Cores: 8}})
	if err != nil {
		return err
	}
	awardedBy := time.Now().UnixNano()
	p, err := f.s.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		return err
	}
	billing := *p.Capability.Billing
	wantStart, wantExecutor := capability.BillingAtAward, "runner"
	if cloud {
		wantStart, wantExecutor = capability.BillingAtStart, "cloud"
		exchanged, err := f.s.ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: p.Capability.Prefix})
		if err != nil {
			return err
		}
		p.Capability = exchanged.Capability
	}
	if billing != *p.Capability.Billing || billing.PayerTeam != string(store.DefaultTeam) || !billing.Metered || billing.Start != wantStart || billing.ExecutorClass != wantExecutor || billing.Unit != capability.BillingJob {
		return fmt.Errorf("award provenance: %+v", billing)
	}
	var intervals int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM billable_intervals`).Scan(&intervals); err != nil || (intervals == 0) != cloud {
		return fmt.Errorf("pre-execution intervals=%d, %v", intervals, err)
	}
	table[0].MicroPerSecond, table[1].MicroPerSecond = 200, 1000
	if _, err := f.s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateTable: &table}); err != nil {
		return err
	}
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Capability.Prefix}, CapabilityPrefix: p.Capability.Prefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	change := store.NodeTransition{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: recoverySourceAttempt(0)}
	starting := time.Now().UnixNano()
	if _, err := f.tenant.TransitionNode(ctx, change); err != nil {
		return err
	}
	startedBy := time.Now().UnixNano()
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	node, err := f.tenant.ReadNode(ctx, change.Ref)
	if err != nil || node.Status != store.StatusRunning || node.Attempt != 1 || node.CapabilityPrefix != actor.CapabilityPrefix || node.StartedAt == nil {
		return fmt.Errorf("persisted execution: %+v, %v", node, err)
	}
	var awarded, started, price, reserved int64
	if err := f.s.DB().QueryRowContext(ctx, `SELECT a.awarded_at,i.started_at,c.rate_micro_per_second,c.amount_micro FROM billing_awards a JOIN billable_intervals i ON i.team=a.team AND i.reservation_id=a.reservation_id JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix WHERE a.team=? AND a.reservation_id=? AND c.kind='reservation'`, store.DefaultTeam, billing.ReservationID).Scan(&awarded, &started, &price, &reserved); err != nil {
		return err
	}
	lower, upper := awarded, awardedBy
	if cloud {
		lower, upper = starting, startedBy
	}
	if started < lower || started > upper || price != 10 || reserved != 600 {
		return fmt.Errorf("billing start=%d outside [%d,%d] price=%d reservation=%d", started, lower, upper, price, reserved)
	}
	return nil
}
