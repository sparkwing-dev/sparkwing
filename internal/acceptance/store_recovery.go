package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	var parentKind, parentID, expansion any
	if c.Parent != nil {
		parentKind, parentID = c.Parent.Kind, c.Parent.ID
	}
	if c.Binding.Expansion != nil {
		expansion = c.Binding.Expansion.Generation
	}
	_, err = s.DB().ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,bind_node,bind_holder,bind_generation,reserved_pools,created_at,created_by,expires_at,repo_scope,metered,repo_key,ref_scope,push_sha,parent_kind,parent_id,eval_unit_generation)
VALUES (?,?,NULLIF(?,''),?,?,'controller',?,'delegate',1,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),?,?,'fixture',?,?,?,?,?,?,?,?,?)`, c.Prefix, hex.EncodeToString(c.Digest[:]), c.Team, c.Subject.Kind, c.Subject.ID, grants, body, c.Binding.Kind, c.Binding.RunID, c.Binding.NodeID, c.Binding.Holder, c.Binding.Generation, pools, c.IssuedAt.UnixNano(), c.ExpiresAt.UnixNano(), repositories, c.RootMetered, repo, ref, sha, parentKind, parentID, expansion)
	return err
}

func (f storeRecoveryFixture) expireHolder(ctx context.Context) error {
	_, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at=? WHERE team=? AND run_id='run' AND node_id='job'`, time.Now().UnixNano(), store.DefaultTeam)
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

func recoverySourceAttempt(attempt int64) *int64 { return &attempt }
