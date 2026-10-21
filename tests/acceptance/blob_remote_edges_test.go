package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func checkBlobRemoteCacheEdges(t testing.TB, ctx context.Context, root string) (err error) {
	s, err := teststore.OpenExisting(filepath.Join(root, "remote-edges.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := teststore.SeedCacheScope(ctx, s)
	if err != nil {
		return err
	}
	var body []byte
	if err := teststore.SQL(t, s).QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", request.Intent.Authority.CapabilityRef).Scan(&body); err != nil {
		return err
	}
	var authority store.CapabilityRecord
	if err := json.Unmarshal(body, &authority); err != nil {
		return err
	}
	authority.Value.Grants = []capability.Grant{capability.DataRead}
	authority.Value.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	body, err = json.Marshal(authority)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(authority.Value.Grants)
	if err != nil {
		return err
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE capabilities SET authority_json=?,grants=?,ref_scope='' WHERE prefix=?", body, grants, authority.Value.Prefix); err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	key := blob.RecordKey{Team: string(store.DefaultTeam), Kind: blob.KindDepcache, Name: "modules", Scope: blob.Scope{Repo: "gh:1", Ref: blob.RefScope{Ref: "main", Verified: true}, Key: "archive"}}
	namespace, err := blob.RecordNamespace(key)
	if err != nil {
		return err
	}
	lookup := blob.RecordLookup{Authority: blob.Authority{CapabilityRef: authority.Value.Prefix}, Key: key}
	for _, verifiedDefault := range []bool{false, true} {
		facts := blob.RefFacts{Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: verifiedDefault, AppDeliveryKey: "fixture-delivery", SHA: "fixture-sha"}
		body, err = json.Marshal(facts)
		if err != nil {
			return err
		}
		if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE blob_records SET kind='depcache',scope=?,name='modules',ref_scope='main',pipeline='',ref_facts_json=?,verified_default_branch=? WHERE team=?", namespace, body, verifiedDefault, store.DefaultTeam); err != nil {
			return err
		}
		hit, err := tenant.LookupRecord(ctx, lookup)
		if hit.Found != verifiedDefault || (verifiedDefault && (err != nil || hit.Record.Digest != request.Intent.Digest || len(hit.ReadSet.Edges) == 0)) || (!verifiedDefault && err != nil && !errors.Is(err, blob.ErrDigestNotReadable)) {
			return fmt.Errorf("repo reader without a job attempt, verified default=%v: %+v: %v", verifiedDefault, hit, err)
		}
	}
	return nil
}
