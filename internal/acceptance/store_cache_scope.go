package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreCacheScopeBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-cache-scope.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := seedStoreCacheScopeBoundary(ctx, s.DB())
	if err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
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

// safety: persisted cache fixtures isolate reference authority from namespace policy and physical uploads.
func seedStoreCacheScopeBoundary(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	request, err := seedStorePresenceAttempts(ctx, db)
	if err != nil {
		return request, err
	}
	attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7}}
	request.Intent.Authority.Attempt, request.Intent.Identity.Attempt = attempt, attempt
	sealed := runspec.SealedRequest{Version: 1, Request: runspec.Request{
		Git:         runspec.Git{Ref: "feature", SHA: "feature-sha", App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Ref: "feature", SHA: "feature-sha", DeliveryKey: "feature-delivery", DefaultBranch: "main"}}},
		PullRequest: runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{BaseRepo: "gh:1", BaseRef: "release"}},
	}}
	body, err := json.Marshal(sealed)
	if err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE runs SET request_json=? WHERE team=? AND id='upload'", body, store.DefaultTeam); err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE nodes SET spec_json=? WHERE team=? AND run_id='upload' AND node_id='a'", []byte(`{"id":"a","memo":{}}`), store.DefaultTeam); err != nil {
		return request, err
	}
	if err := db.QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", request.Intent.Authority.CapabilityRef).Scan(&body); err != nil {
		return request, err
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return request, err
	}
	record.Value.Data.WriteRef, record.Value.Data.ReadRefs = "feature", []string{"feature", "release", "main"}
	body, err = json.Marshal(record)
	if err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE capabilities SET authority_json=?,ref_scope='feature' WHERE prefix=?", body, request.Intent.Authority.CapabilityRef); err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,3,1)", store.DefaultTeam, request.Intent.Digest); err != nil {
		return request, err
	}
	source, err := json.Marshal(request.Intent.Identity)
	if err != nil {
		return request, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,source_node_id,source_attempt,source_generation,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,created_at) VALUES (?,'memo','fixture','output',?,'gh:1','feature','publisher','local',?,'publication','upload','a',1,7,1,0,'fixture-sha',?,'fixture',1)`, store.DefaultTeam, request.Intent.Digest, source, []byte(`{}`))
	return request, err
}
