package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreQueueOrder checks scoped trigger selection and soft job preference without queue mutation on mismatch.
func CheckStoreQueueOrder(ctx context.Context, root string) error {
	for _, preferred := range []bool{false, true} {
		if err := checkStoreQueueOrder(ctx, filepath.Join(root, fmt.Sprintf("%t.db", preferred)), preferred); err != nil {
			return fmt.Errorf("preferred=%t: %w", preferred, err)
		}
	}
	return nil
}

func checkStoreQueueOrder(ctx context.Context, path string, preferred bool) (err error) {
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	now := time.Now().UTC()
	demand, err := seedStoreQueueOrder(ctx, f, now)
	if err != nil {
		return err
	}
	if err := checkStoreQueueCounts(ctx, f.s, map[string]int{store.QueueStateReady: 3}); err != nil {
		return err
	}
	claim, err := f.s.ClaimUnit(ctx, demand)
	if err != nil || claim.RunID != "run" || claim.Kind != capability.UnitTrigger {
		return fmt.Errorf("scoped trigger: %s/%s, %v", claim.Kind, claim.RunID, err)
	}
	if _, err := f.s.ClaimUnit(ctx, demand); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("foreign push remained claimable: %v", err)
	}
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	order := []string{"job", "z-preferred"}
	if preferred {
		demand.Labels = []string{"gpu"}
		order[0], order[1] = order[1], order[0]
	}
	for _, id := range order {
		claim, err := f.s.ClaimUnit(ctx, demand)
		if err != nil || claim.NodeID != id {
			return fmt.Errorf("job order wants %s, got %s/%s: %v", id, claim.RunID, claim.NodeID, err)
		}
	}
	if err := checkStoreQueueCounts(ctx, f.s, map[string]int{store.QueueStateReady: 1, store.QueueStateClaimed: 2}); err != nil {
		return err
	}
	for range 2 {
		if _, err := f.s.ClaimUnit(ctx, demand); !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("ineligible queue: %v", err)
		}
	}
	blocked, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "a-blocked"})
	if err != nil {
		return err
	}
	if blocked.ReadyAt == nil || !blocked.ReadyAt.Equal(now) || blocked.ClaimedBy != "" || blocked.StateSeq != 0 {
		return errors.New("mismatch changed blocked job's position or state")
	}
	demand.Labels = []string{"unavailable"}
	claim, err = f.s.ClaimUnit(ctx, demand)
	if err != nil || claim.NodeID != "a-blocked" {
		return fmt.Errorf("later eligible claimant: %s/%s, %v", claim.RunID, claim.NodeID, err)
	}
	return checkStoreQueueCounts(ctx, f.s, map[string]int{store.QueueStateClaimed: 3})
}

func checkStoreQueueCounts(ctx context.Context, s *store.Store, want map[string]int) error {
	for _, state := range store.QueueStates() {
		want[state] += 0
	}
	counts, err := s.AsOperator().CountNodesByQueueState(ctx)
	if err != nil || !reflect.DeepEqual(counts, want) {
		return fmt.Errorf("queue states: %v, %v; want %v", counts, err, want)
	}
	ready, err := s.AsOperator().CountPendingNodes(ctx)
	if err != nil || ready != want[store.QueueStateReady] {
		return fmt.Errorf("ready count: %d, %v; want %d", ready, err, want[store.QueueStateReady])
	}
	return nil
}

func seedStoreQueueOrder(ctx context.Context, f storeRecoveryFixture, now time.Time) (store.UnitDemand, error) {
	until := now.Add(time.Hour)
	root := capability.Capability{Digest: sha256.Sum256([]byte("queue-root")), Prefix: "queue-root", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Repositories: []string{"gh:1"}, Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: "sha", WorkflowRunID: "workflow"}}, Issuance: capability.Delegate, IssuedAt: now, ExpiresAt: &until}
	if err := seedRecoveryCapability(ctx, f.s, root); err != nil {
		return store.UnitDemand{}, err
	}
	f.s.SetClaimProfileResolver(&matchingEnrollment{profile: match.Profile{Capacity: match.Resources{Cores: 8}}})
	request := runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: "sha"}}}
	body, err := json.Marshal(request)
	if err != nil {
		return store.UnitDemand{}, err
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE runs SET request_json=?,spec_json=NULL,spec_hash='',sealed_at=NULL,plan_json=NULL,plan_hash='',git_branch='main',git_sha='sha',created_at=?`, []any{body, now.UnixNano()}},
		{`UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL`, nil},
		{`UPDATE nodes SET claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,ready_at=?,execution_json=?`, []any{now.UnixNano(), []byte(`{}`)}},
		{`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES (?,'run','z-preferred','pending',?,?,?)`, []any{store.DefaultTeam, now.UnixNano(), []byte(`{"id":"z-preferred"}`), []byte(`{"prefers":{"set":true,"value":["gpu"]}}`)}},
		{`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES (?,'run','a-blocked','pending',?,?,?)`, []any{store.DefaultTeam, now.UnixNano(), []byte(`{"id":"a-blocked"}`), []byte(`{"requires":{"set":true,"value":["unavailable"]}}`)}},
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,'foreign',2,'fixture','pending',?,'request',1,'gh:2','host','execute',?,'host',1,'r30',30)`, []any{store.DefaultTeam, []byte(`{"version":1,"request":{"git":{"repository":{"key":"gh:2"},"ref":"main","sha":"sha"}}}`), []byte(`[]`)}},
		{`INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES (?,'foreign','pending','trigger',1,1,?,?)`, []any{store.DefaultTeam, []byte(`[]`), []byte(`[]`)}},
	} {
		if _, err := f.s.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			return store.UnitDemand{}, err
		}
	}
	demand := store.UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}, Available: match.Resources{Cores: 8}}
	return demand, nil
}
