package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreReferenceBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-reference-edges.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	authority, digests, err := seedStoreReferenceBoundary(ctx, s.DB())
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
	reader := authority.Attempt.Value
	authorized := capability.Authorized{Principal: capability.Principal{Capability: capability.Capability{Prefix: authority.CapabilityRef, Team: reader.Team}}}
	for _, test := range []struct {
		name    string
		spec    nodeproto.JobSpec
		allowed bool
	}{
		{"unrelated", nodeproto.JobSpec{}, false},
		{"dependency", nodeproto.JobSpec{Needs: []string{"a"}}, true},
		{"optional_dependency", nodeproto.JobSpec{NeedsIfPresent: []string{"a"}}, true},
		{"transitive_dependency", nodeproto.JobSpec{Needs: []string{"unrelated"}}, true},
		{"artifact_input", nodeproto.JobSpec{Consumes: []nodeproto.ArtifactInput{{Producer: "a"}}}, true},
		{"generated_group", nodeproto.JobSpec{NeedsGenerated: []string{"generated"}}, true},
		{"failure_handler", nodeproto.JobSpec{OnFailureOf: "a"}, true},
	} {
		test.spec.ID = reader.NodeID
		body, err := json.Marshal(test.spec)
		if err != nil {
			return err
		}
		if _, err := s.DB().ExecContext(ctx, "UPDATE nodes SET spec_json=? WHERE team=? AND run_id='upload' AND node_id='b'", body, store.DefaultTeam); err != nil {
			return err
		}
		presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: authority, Digests: digests})
		var want []blob.Digest
		if test.allowed {
			want = digests[:3]
		}
		if err != nil || !slices.Equal(presence.Present, want) {
			return fmt.Errorf("%s read set=%+v, want %v: %v", test.name, presence, want, err)
		}
		for i, digest := range digests {
			for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
				target := capability.DataTarget{Operation: operation, Digest: string(digest), RecordKind: string(blob.KindOutput), RunID: reader.RunID, NodeID: reader.NodeID, Attempt: reader.Attempt, Generation: reader.Generation}
				err := tenant.CheckDataAccess(ctx, authorized, target)
				allowed := test.allowed && i < 3
				if allowed && err != nil || !allowed && err == nil {
					return fmt.Errorf("%s %s digest %d allowed=%v: %v", test.name, operation, i, allowed, err)
				}
			}
		}
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("reference checks consumed %s bytes", table)
		}
	}
	return nil
}

// safety: persisted graph fixtures isolate edge decisions from plan intake and verified manifest bytes.
func seedStoreReferenceBoundary(ctx context.Context, db *sql.DB) (blob.Authority, []blob.Digest, error) {
	request, err := seedStorePresenceAttempts(ctx, db)
	if err != nil {
		return blob.Authority{}, nil, err
	}
	reader := runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "b", Attempt: 1, Generation: 7}
	authority := request.Intent.Authority
	authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: reader}
	source := request.Intent.Identity
	reader.NodeID = "a"
	source.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: reader}
	body, err := json.Marshal(source)
	if err != nil {
		return authority, nil, err
	}
	var digests []blob.Digest
	for _, letter := range []string{"a", "b", "c", "d"} {
		digest := blob.Digest(strings.Repeat(letter, 64))
		digests = append(digests, digest)
		if _, err := db.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,3,1)", store.DefaultTeam, digest); err != nil {
			return authority, nil, err
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE nodes SET status='done',outcome='success',finished_at=1,spec_json=? WHERE team=? AND run_id='upload' AND node_id='a'`, []any{[]byte(`{"id":"a","groups":["generated"]}`), store.DefaultTeam}},
		{`UPDATE node_attempts SET finished_at=1,outcome='success' WHERE team=? AND run_id='upload' AND node_id='a'`, []any{store.DefaultTeam}},
		{`UPDATE nodes SET spec_json=? WHERE team=? AND run_id='upload' AND node_id='unrelated'`, []any{[]byte(`{"id":"unrelated","deps":["a"]}`), store.DefaultTeam}},
		{`INSERT INTO blob_records(team,kind,scope,name,digest,manifest_digest,run_id,node_id,attempt,generation,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,source_node_id,source_attempt,source_generation,verified_ref,verified_default_branch,ref_facts_json,pipeline,created_at) VALUES (?,'output','fixture','result',?,?,'upload','a',1,7,'gh:1','manual:','publisher','local',?,'publication','upload','a',1,7,0,0,?,'fixture',1)`, []any{store.DefaultTeam, digests[0], digests[1], body, []byte(`{}`)}},
		{`INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)`, []any{store.DefaultTeam, digests[1], digests[2]}},
		{`INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`, nil},
		{`INSERT INTO blobs(team,digest,size,created_at) VALUES ('foreign',?,3,1)`, []any{strings.Repeat("e", 64)}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return authority, nil, err
		}
	}
	digests = append(digests, blob.Digest(strings.Repeat("e", 64)), blob.Digest(strings.Repeat("f", 64)))
	return authority, digests, nil
}
