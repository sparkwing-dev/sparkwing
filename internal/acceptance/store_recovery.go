package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreRecoveryBoundaryScenarios lists SQLite persistence proofs, separate from native execution cells.
func StoreRecoveryBoundaryScenarios() []string {
	return []string{"recovery-cancel-beats-loss", "recovery-reconciler-waits-and-reclaims", "recovery-unstarted-lapse", "recovery-started-lapse-cap", "recovery-concurrency-continuous-adoption", "recovery-concurrency-no-revival", "recovery-application-retry-ordinal", "recovery-local-orphans"}
}

// CheckStoreRecoveryBoundary exercises the final transitions, reaper and reconciler against SQLite.
func CheckStoreRecoveryBoundary(ctx context.Context, root, id string) error {
	switch id {
	case "recovery-concurrency-no-revival":
		return checkRecoveryCapacityMaintenance(ctx, root)
	case "recovery-local-orphans":
		return checkRecoveryLocalOrphans(ctx, root)
	case "recovery-application-retry-ordinal":
		return checkRecoveryApplicationRetry(ctx, root)
	case "recovery-cancel-beats-loss":
		return checkRecoveryCancellation(ctx, root)
	case "recovery-unstarted-lapse":
		return checkRecoveryUnstarted(ctx, root)
	case "recovery-started-lapse-cap":
		return checkRecoveryStartedCap(ctx, root)
	case "recovery-concurrency-continuous-adoption":
		return checkRecoveryCapacity(ctx, root)
	case "recovery-reconciler-waits-and-reclaims":
		for _, order := range []string{"host_first", "reaper_first"} {
			if err := checkRecoveryReconciliation(ctx, root, order); err != nil {
				return fmt.Errorf("%s: %w", order, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown store recovery boundary %q", id)
	}
}

type storeRecoveryFixture struct {
	s      *store.Store
	tenant *store.Tenant
	owner  store.Actor
	holder store.Actor
	lease  store.SingletonLease
}

type recoveryEnrollment struct{}

func (recoveryEnrollment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 8}
	return match.Profile{Capacity: capacity}, capacity, nil
}

func newStoreRecoveryFixture(ctx context.Context, path string, started bool) (_ storeRecoveryFixture, err error) {
	s, err := store.Open(path)
	if err != nil {
		return storeRecoveryFixture{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	f := storeRecoveryFixture{s: s}
	s.SetClaimProfileResolver(recoveryEnrollment{})
	f.tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return f, err
	}
	now, until := time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash)
VALUES (?,'run',1,'fixture','running',?,'request',?,'gh:1','host','execute',?,'host',?,'r30',30,?,'spec',?,?,'plan')`, []any{store.DefaultTeam, []byte(`{"version":1}`), now.UnixNano(), []byte(`[]`), now.UnixNano(), []byte(`{}`), now.UnixNano(), []byte(`{"jobs":[{"id":"job"}]}`)}},
		{`INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels)
VALUES (?,'run','claimed','trigger',?,?,'host','owner',1,?,?,?)`, []any{store.DefaultTeam, now.UnixNano(), now.UnixNano(), until.UnixNano(), []byte(`[]`), []byte(`[]`)}},
		{`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,ready_at,lease_expires_at)
VALUES (?,'run','job','pending',?,?,'runner','holder',2,?,?)`, []any{store.DefaultTeam, []byte(`{"id":"job"}`), []byte(`{}`), now.UnixNano(), until.UnixNano()}},
	}
	for _, statement := range statements {
		if _, err := s.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			return f, err
		}
	}
	for _, unit := range []struct {
		prefix, subject string
		binding         capability.Binding
		grants          []capability.Grant
	}{
		{"owner", "host", capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 1}, []capability.Grant{capability.TriggerHost, capability.NodesClaim}},
		{"holder", "runner", capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}, []capability.Grant{capability.NodeReport, capability.NodeResult}},
	} {
		c := capability.Capability{Digest: sha256.Sum256([]byte(unit.prefix)), Prefix: unit.prefix, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: unit.subject}, Audience: capability.Controller, Grants: unit.grants, Binding: unit.binding, Issuance: capability.Delegate, IssuedAt: now, ExpiresAt: &until, ReservedPools: []string{}, Repositories: []string{}}
		if err := seedRecoveryCapability(ctx, s, c); err != nil {
			return f, err
		}
	}
	f.owner = store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, CapabilityPrefix: "owner", Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Holder: "host", Generation: 1}}
	f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "holder"}, CapabilityPrefix: "holder", Holder: "runner", Generation: 2}
	f.lease, err = s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "recovery", time.Minute)
	if err != nil {
		return f, err
	}
	if started {
		if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: recoverySourceAttempt(0)}); err != nil {
			return f, err
		}
	}
	return f, nil
}

func seedRecoveryCapability(ctx context.Context, s *store.Store, c capability.Capability) error {
	record := store.CapabilityRecord{Version: 1, Value: c}
	if err := record.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return err
	}
	pools, err := json.Marshal(c.ReservedPools)
	if err != nil {
		return err
	}
	repositories, err := json.Marshal(c.Repositories)
	if err != nil {
		return err
	}
	repo, ref, sha := "", "", ""
	if c.Data != nil {
		repo, ref = c.Data.RepositoryKey, c.Data.WriteRef
	}
	if c.Binding.Push != nil {
		repo, ref, sha = c.Binding.Push.RepositoryKey, c.Binding.Push.Ref, c.Binding.Push.SHA
	}
	_, err = s.DB().ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,bind_node,bind_holder,bind_generation,reserved_pools,created_at,created_by,expires_at,repo_scope,metered,repo_key,ref_scope,push_sha)
VALUES (?,?,?,'machine',?,'controller',?,'delegate',1,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),?,?,'fixture',?,?,?,?,?,?)`, c.Prefix, hex.EncodeToString(c.Digest[:]), c.Team, c.Subject.ID, grants, body, c.Binding.Kind, c.Binding.RunID, c.Binding.NodeID, c.Binding.Holder, c.Binding.Generation, pools, c.IssuedAt.UnixNano(), c.ExpiresAt.UnixNano(), repositories, c.RootMetered, repo, ref, sha)
	return err
}

func (f storeRecoveryFixture) expireHolder(ctx context.Context) error {
	_, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at=1 WHERE team=? AND run_id='run' AND node_id='job'`, store.DefaultTeam)
	return err
}

func (f *storeRecoveryFixture) reopen(ctx context.Context, path string) error {
	if err := f.s.Close(); err != nil {
		return err
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	f.s = s
	s.SetClaimProfileResolver(recoveryEnrollment{})
	f.tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	return err
}

func (f storeRecoveryFixture) refuseLateReports(ctx context.Context) error {
	before, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	for _, actor := range []store.Actor{f.owner, f.holder} {
		change := store.NodeTransition{Ref: before.Ref, Actor: actor, To: store.NodeSuccess, Cause: store.CauseReport}
		if actor.Kind == store.ActorHolder {
			change.SourceAttempt = recoverySourceAttempt(before.Attempt)
		}
		if _, err := f.tenant.TransitionNode(ctx, change); err == nil {
			return errors.New("late holder or adopting host overwrote the job")
		}
	}
	after, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("refused late report changed persisted job")
	}
	return nil
}

func checkRecoveryCancellation(ctx context.Context, root string) (err error) {
	f, err := newStoreRecoveryFixture(ctx, filepath.Join(root, "cancel.db"), true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	actor := store.Actor{Kind: store.ActorReaper, Owner: store.OwnerLeaseRef{Kind: store.OwnerController, Name: f.lease.Name, Holder: f.lease.Holder, Generation: f.lease.Generation}}
	request := store.RunTransition{RunID: "run", Actor: actor, To: store.RunCancelled, Cause: store.RunCancel, Reason: "user_cancelled"}
	for range 2 {
		if _, err := f.tenant.TransitionRun(ctx, request); err != nil {
			return err
		}
	}
	row, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	if row.Status != store.StatusRunning || row.ClaimedBy != "runner" || row.CancelRequestedAt == nil || row.FinishedAt != nil || row.CancelReason != "user_cancelled" {
		return fmt.Errorf("cancellation stole a live holder: %+v", row)
	}
	if err := f.expireHolder(ctx); err != nil {
		return err
	}
	units, err := f.s.ReapUnits(ctx, f.lease, 10)
	if err != nil {
		return err
	}
	var cancelled bool
	for _, unit := range units {
		if unit.NodeID == "job" {
			cancelled = unit.Cancelled && !unit.Requeued && !unit.Failed
		}
	}
	row, err = f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	if !cancelled || row.Outcome != store.NodeCancelled || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.Disruptions != 0 || row.Attempt != 1 {
		return fmt.Errorf("loss overrode cancellation: %+v, %+v", units, row)
	}
	if err := f.reopen(ctx, filepath.Join(root, "cancel.db")); err != nil {
		return err
	}
	row, err = f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		return err
	}
	if row.CancelReason != "user_cancelled" {
		return errors.New("reopening lost cancellation intent")
	}
	if err := f.refuseLateReports(ctx); err != nil {
		return err
	}
	if err := checkRecoveryEvents(ctx, f, []string{"job_started", "job_cancel_requested", "job_cancelled"}); err != nil {
		return err
	}
	var runs, attempts int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE team=?`, store.DefaultTeam).Scan(&runs); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id='run'`, store.DefaultTeam).Scan(&attempts); err != nil {
		return err
	}
	if runs != 1 || attempts != 1 {
		return fmt.Errorf("cancellation created replacement work: runs=%d attempts=%d", runs, attempts)
	}
	return nil
}

func checkRecoveryReconciliation(ctx context.Context, root, order string) (err error) {
	f, err := newStoreRecoveryFixture(ctx, filepath.Join(root, order+".db"), true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	request := store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: f.owner, Policy: store.SourceResume}
	for range 2 {
		got, err := f.tenant.ReconcileNode(ctx, request)
		if err != nil {
			return err
		}
		if got.Kind != store.WaitHolder || got.Claim != nil || len(got.Bearer) != 0 || got.Node.Generation != 2 {
			return fmt.Errorf("live or unreaped holder was adopted as a result: %+v", got)
		}
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
	}
	if order == "host_first" {
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
	}
	units, err := f.s.ReapUnits(ctx, f.lease, 10)
	if err != nil {
		return err
	}
	if len(units) != 1 || !units[0].Requeued || units[0].Disruptions != 1 {
		return fmt.Errorf("reaper did not requeue in the same run: %+v", units)
	}
	if err := f.reopen(ctx, filepath.Join(root, order+".db")); err != nil {
		return err
	}
	if err := f.refuseLateReports(ctx); err != nil {
		return err
	}
	probe := request
	probe.Policy, probe.MaterializeOnly = store.SourceMemo, true
	miss, err := f.tenant.ReconcileNode(ctx, probe)
	if err != nil {
		return err
	}
	if miss.Kind != store.Dispatch || miss.Claim != nil || len(miss.Bearer) != 0 || miss.Node.Status != store.StatusPending || miss.Node.StateSeq != 2 {
		return fmt.Errorf("missing memo fabricated a result or execution award: %+v", miss)
	}
	var wg sync.WaitGroup
	results := make([]store.ReconciledNode, 2)
	failures := make([]error, 2)
	start := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-start
			results[i], failures[i] = f.tenant.ReconcileNode(ctx, request)
		})
	}
	close(start)
	wg.Wait()
	if err := errors.Join(failures...); err != nil {
		return err
	}
	var execute, wait int
	for _, result := range results {
		switch result.Kind {
		case store.Execute:
			execute++
			if result.Claim == nil || len(result.Bearer) == 0 || result.Claim.Generation != 3 || result.Node.Disruptions != 1 || result.Node.ApplicationRetries != 0 || result.Node.Attempt != 1 {
				return fmt.Errorf("reclaim lost history or its fence: %+v", result)
			}
		case store.WaitHolder:
			wait++
			if result.Claim != nil || len(result.Bearer) != 0 {
				return errors.New("second reconciliation received execution authority")
			}
		default:
			return fmt.Errorf("requeue reconciliation returned %s", result.Kind)
		}
	}
	if execute != 1 || wait != 1 {
		return fmt.Errorf("racing reconciliation executes=%d waits=%d", execute, wait)
	}
	return checkRecoveryEvents(ctx, f, []string{"job_started", "job_requeued", "job_ready", "job_claimed"})
}

func checkRecoveryEvents(ctx context.Context, f storeRecoveryFixture, want []string) error {
	events, err := f.tenant.ReadStateEvents(ctx, "run", 0, 100)
	if err != nil {
		return err
	}
	var kinds []string
	for _, event := range events {
		if event.NodeID != "job" {
			continue
		}
		kinds = append(kinds, event.Kind)
		if event.StateSeq != int64(len(kinds)) {
			return fmt.Errorf("job event sequence=%d at %s", event.StateSeq, event.Kind)
		}
	}
	if !reflect.DeepEqual(kinds, want) {
		return fmt.Errorf("job events=%v, want %v", kinds, want)
	}
	return nil
}
