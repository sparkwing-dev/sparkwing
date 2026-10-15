package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type matchingEnrollment struct{ profile match.Profile }

func (e *matchingEnrollment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	return e.profile, e.profile.Capacity, nil
}

// CheckStoreClaimMatching checks named and queued awards against persisted authority and the shared matcher.
func CheckStoreClaimMatching(ctx context.Context, root string) error {
	for _, named := range []bool{false, true} {
		for _, test := range []string{"capacity", "name", "missing_labels", "named_claim", "metered_runner", "pool_label", "pool_toleration", "trigger_requirements"} {
			if err := checkStoreMatching(ctx, filepath.Join(root, fmt.Sprintf("%t-%s.db", named, test)), named, test); err != nil {
				return fmt.Errorf("%s/named=%t: %w", test, named, err)
			}
		}
	}
	return nil
}

func checkStoreMatching(ctx context.Context, path string, named bool, test string) (err error) {
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,execution_json=?`, []byte(`{}`)); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET repo_display='https://github.com/acme/repo'`); err != nil {
		return err
	}
	until := time.Now().UTC().Add(time.Hour)
	root := capability.Capability{Digest: sha256.Sum256([]byte("matching-root")), Prefix: "matching-root", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: time.Now().UTC(), ExpiresAt: &until}
	enrollment := &matchingEnrollment{profile: match.Profile{Capacity: match.Resources{Cores: 8}}}
	demand := store.UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Available: match.Resources{Cores: 8}}
	unit := store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	execution := runspec.JobExecutionSettings{}
	switch test {
	case "capacity":
		enrollment.profile.Capacity.Cores = 2
		execution.Resources.Value.Cores.Value = 4
		execution.Resources.Value.Cores.Set = true
		execution.Resources.Set = true
	case "name":
		execution.Requires.Value = []string{"name=other"}
		demand.Labels = []string{"name=other"}
	case "missing_labels":
		execution.Requires.Value = []string{"gpu"}
	case "named_claim", "metered_runner":
		root.RootMetered = test == "metered_runner"
		enrollment.profile.Accept, err = match.ParseAccept([]string{"github.com/other/*"})
		if test == "named_claim" {
			execution.Requires.Value = []string{"gpu"}
			demand.Labels = []string{"gpu"}
		}
	case "pool_label":
		execution.Requires.Value = []string{"pool=prod"}
		demand.Labels = []string{"pool=prod"}
		enrollment.profile.ReservedPools = []string{"prod"}
	case "pool_toleration":
		root.ReservedPools = []string{"prod"}
		execution.Requires.Value = []string{"pool=prod"}
	case "trigger_requirements":
		unit = store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
		demand.Kinds = []capability.UnitKind{capability.UnitTrigger}
		request := runspec.SealedRequest{}
		request.Request.Options.Requires.Value = []string{"gpu"}
		request.Request.Options.Requires.Set = true
		body, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET request_json=?,spec_json=NULL,spec_hash='',sealed_at=NULL,plan_json=NULL,plan_hash=''`, body); err != nil {
			return err
		}
		if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL`); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	if named {
		demand.Target = &unit
	}
	execution.Requires.Set = len(execution.Requires.Value) > 0
	body, err := json.Marshal(execution)
	if err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET execution_json=?`, body); err != nil {
		return err
	}
	if err := seedRecoveryCapability(ctx, f.s, root); err != nil {
		return err
	}
	f.s.SetClaimProfileResolver(enrollment)
	_, refused := f.s.ClaimUnit(ctx, demand)
	if refused == nil {
		return errors.New("unmatched or unauthorized claimant received an award")
	}
	var denied *apierr.Error
	if named && (!errors.As(refused, &denied) || denied.Code != apierr.Forbidden && denied.Code != apierr.LockHeld) || !named && !errors.Is(refused, store.ErrNotFound) {
		return fmt.Errorf("claim failed outside matching: %w", refused)
	}
	for _, table := range []string{"billing_awards", "events"} {
		var count int
		if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("refused claim wrote %d %s rows", count, table)
		}
	}
	proceed, err := allowMatchingClaim(ctx, f, enrollment, &demand, execution, test)
	if err != nil || !proceed {
		return err
	}
	claim, err := f.s.ClaimUnit(ctx, demand)
	if err != nil {
		return err
	}
	if claim.Award.Holder != root.Subject.ID || claim.Team != store.DefaultTeam || claim.RunID != unit.RunID || claim.Kind != unit.Kind || len(claim.Bearer) == 0 {
		return errors.New("matched award lost server-derived identity")
	}
	return nil
}

func allowMatchingClaim(ctx context.Context, f storeRecoveryFixture, enrollment *matchingEnrollment, demand *store.UnitDemand, execution runspec.JobExecutionSettings, test string) (bool, error) {
	var err error
	switch test {
	case "capacity":
		enrollment.profile.Capacity.Cores = 8
	case "name":
		execution.Requires.Value = []string{"name=enrolled"}
		body, marshalErr := json.Marshal(execution)
		err = marshalErr
		if err == nil {
			_, err = f.s.DB().ExecContext(ctx, `UPDATE nodes SET execution_json=?`, body)
		}
	case "missing_labels", "trigger_requirements":
		demand.Labels = []string{"gpu"}
	case "named_claim":
		enrollment.profile.Accept, err = match.ParseAccept([]string{"github.com/acme/*"})
		if err == nil {
			demand.Labels = nil
			if _, err := f.s.ClaimUnit(ctx, *demand); err == nil {
				return false, errors.New("repository acceptance bypassed a missing job requirement")
			}
			demand.Labels = []string{"gpu"}
		}
	case "metered_runner", "pool_label":
		return false, nil
	case "pool_toleration":
		_, err = f.s.DB().ExecContext(ctx, `UPDATE runs SET allowed_pools=?`, []byte(`["prod"]`))
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
