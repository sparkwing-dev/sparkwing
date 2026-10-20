package teststore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SeedUpload seeds a publication authority and its run.
func SeedUpload(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	now := time.Now().UTC()
	c := capability.Capability{
		Digest: sha256.Sum256([]byte("upload-boundary")), Prefix: "upload-boundary", Team: string(store.DefaultTeam),
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "publisher"}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.DataRead, capability.DataWrite}, Issuance: capability.Delegate,
		Binding: capability.Binding{Kind: capability.BindingRun, RunID: "upload"}, IssuedAt: now,
		ReservedPools: []string{}, Repositories: []string{},
		Data: &capability.DataScope{Use: capability.DataPublish, RepositoryKey: "gh:1", RunID: "upload"},
	}
	record := store.CapabilityRecord{Version: 1, Value: c}
	if err := record.Validate(); err != nil {
		return store.UploadReservationRequest{}, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,'upload',1,'fixture','running',?,'fixture',?,'gh:1','host','publish',?,'publisher',?,'r30',30)`, store.DefaultTeam, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"fixture","writer":"account/fixture"}}`), now.UnixNano(), []byte(`[]`), now.UnixNano()); err != nil {
		return store.UploadReservationRequest{}, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,reserved_pools,created_at,created_by,repo_key,repo_scope)
VALUES (?,?,?,'machine','publisher','controller',?,'delegate',1,?,'run','upload',?,?,'publisher','gh:1',?)`, c.Prefix, hex.EncodeToString(c.Digest[:]), store.DefaultTeam, grants, body, []byte(`[]`), now.UnixNano(), []byte(`[]`))
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	identity := blob.UploadIdentity{Kind: blob.UploadPublication, Team: c.Team, RunID: "upload", Principal: "publisher", CapabilityRef: c.Prefix}
	return store.UploadReservationRequest{ID: "committed", Intent: blob.UploadIntent{Authority: blob.Authority{CapabilityRef: c.Prefix}, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: now.Add(time.Hour)}, nil
}

// SeedPresence adds terminal imported attempts to a publication fixture.
func SeedPresence(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	request, err := SeedUpload(ctx, db)
	if err != nil {
		return request, err
	}
	for _, job := range []string{"a", "b", "unrelated"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,attempt,generation,spec_json,execution_json) VALUES (?,'upload',?,'done','success',2,1,7,?,?)`, store.DefaultTeam, job, []byte(`{}`), []byte(`{}`)); err != nil {
			return request, err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES (?,'upload',?,1,7,'publisher','upload-boundary','local','publisher','local',1,2,'success')`, store.DefaultTeam, job); err != nil {
			return request, err
		}
	}
	return request, nil
}

// FeatureSHA is the verified commit SeedCacheScope seals.
const FeatureSHA = "fea7fea7fea7fea7fea7fea7fea7fea7fea7fea7"

// SeedCacheScope seeds verified cache records for read-policy tests.
func SeedCacheScope(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	request, err := SeedPresence(ctx, db)
	if err != nil {
		return request, err
	}
	attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7}}
	request.Intent.Authority.Attempt, request.Intent.Identity.Attempt = attempt, attempt
	app := runspec.AppFacts{Event: "pull_request", RepositoryID: 1, Ref: "refs/heads/feature", SHA: FeatureSHA, DeliveryKey: "feature-delivery", DefaultBranch: "main", VerifiedAt: time.Unix(1, 0).UTC()}
	pr := runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{BaseRepo: "gh:1", BaseRef: "release"}}
	provenance, err := runspec.AppProvenance(app, pr, "fixture")
	if err != nil {
		return request, err
	}
	sealed := runspec.SealedRequest{Version: 1, Provenance: provenance, Request: runspec.Request{
		Git:         runspec.Git{Ref: app.Ref, SHA: app.SHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: app}},
		PullRequest: pr,
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
	record.Value.Data.WriteRef, record.Value.Data.ReadRefs = "refs/heads/feature", []string{"refs/heads/feature", "refs/heads/release", "refs/heads/main"}
	body, err = json.Marshal(record)
	if err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE capabilities SET authority_json=?,ref_scope='refs/heads/feature' WHERE prefix=?", body, request.Intent.Authority.CapabilityRef); err != nil {
		return request, err
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,3,1)", store.DefaultTeam, request.Intent.Digest); err != nil {
		return request, err
	}
	source, err := json.Marshal(request.Intent.Identity)
	if err != nil {
		return request, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,source_node_id,source_attempt,source_generation,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,created_at) VALUES (?,'memo','fixture','output',?,'gh:1','refs/heads/feature','publisher','local',?,'publication','upload','a',1,7,1,0,'fixture-sha',?,'fixture',1)`, store.DefaultTeam, request.Intent.Digest, source, []byte(`{}`))
	return request, err
}
