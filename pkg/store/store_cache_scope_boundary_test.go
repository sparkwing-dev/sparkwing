package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func checkStoreCacheScopeBoundary(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := teststore.SeedCacheScope(ctx, s.DB())
	if err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen()
	if err != nil {
		return err
	}
	s = reopened
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		for _, test := range []struct {
			ref, defaultBranch string
			verifiedDefault    bool
			allowed            bool
		}{
			{"feature", "main", false, true},
			{"release", "main", false, kind == blob.KindDepcache},
			{"release", "release", true, kind == blob.KindDepcache},
			{"main", "main", false, false},
			{"main", "main", true, true},
		} {
			facts := blob.RefFacts{Scope: blob.RefScope{Ref: test.ref, Verified: true}, SHA: "fixture-sha", AppDeliveryKey: "fixture-delivery", DefaultBranch: test.defaultBranch, DefaultBranchVerified: test.verifiedDefault}
			encoded, err := json.Marshal(facts)
			if err != nil {
				return err
			}
			if _, err := s.DB().ExecContext(ctx, `UPDATE blob_records SET kind=?,ref_scope=?,verified_default_branch=?,ref_facts_json=? WHERE team=?`, kind, test.ref, test.verifiedDefault, encoded, store.DefaultTeam); err != nil {
				return err
			}
			presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: request.Intent.Authority, Digests: []blob.Digest{request.Intent.Digest}})
			if err != nil || (len(presence.Present) == 1) != test.allowed {
				return fmt.Errorf("%s/%s default=%v presence=%+v, allowed=%v: %v", kind, test.ref, test.verifiedDefault, presence, test.allowed, err)
			}
			principal := capability.Principal{Capability: capability.Capability{Prefix: request.Intent.Authority.CapabilityRef, Team: string(store.DefaultTeam)}}
			a := request.Intent.Identity.Attempt.Value
			for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
				target := capability.DataTarget{Operation: operation, Digest: string(request.Intent.Digest), RunID: a.RunID, NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation}
				err := tenant.CheckDataAccess(ctx, capability.Authorized{Principal: principal}, target)
				if test.allowed && err != nil || !test.allowed && !errors.Is(err, blob.ErrDigestNotReadable) {
					return fmt.Errorf("%s/%s default=%v %s allowed=%v: %v", kind, test.ref, test.verifiedDefault, operation, test.allowed, err)
				}
			}
		}
	}
	return nil
}

func TestStoreCacheScope(t *testing.T) {
	if err := checkStoreCacheScopeBoundary(t); err != nil {
		t.Fatal(err)
	}
}
