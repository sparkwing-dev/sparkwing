package acceptance

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
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: preseeded authority isolates persistence from root provisioning and physical verification.
func seedStoreUploadBoundary(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
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
VALUES (?,'upload',1,'fixture','running',?,'fixture',?,'gh:1','host','publish',?,'publisher',?,'r30',30)`, store.DefaultTeam, []byte(`{"version":1}`), now.UnixNano(), []byte(`[]`), now.UnixNano()); err != nil {
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
