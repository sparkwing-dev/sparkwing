package acceptance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreLogBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-logs.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	commit, err := seedStoreLogBoundary(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	first := store.Segment{Index: 0, Size: 3, Digest: strings.Repeat("a", 64)}
	first.ObjectKey, err = store.LogSegmentObjectKey(commit.Subject, commit.RetentionClass, first.Index)
	if err != nil {
		return err
	}
	commit.Segments = []store.Segment{first}
	for range 2 {
		if err := tenant.RecordSegments(ctx, commit); err != nil {
			return err
		}
	}
	bad := commit
	bad.Segments = append([]store.Segment(nil), commit.Segments...)
	bad.Segments[0].Digest = strings.Repeat("b", 64)
	if err := tenant.RecordSegments(ctx, bad); err == nil {
		return errors.New("log replay replaced immutable content")
	}
	second := store.Segment{Index: 1, Offset: 3, Size: 2, Digest: strings.Repeat("b", 64)}
	second.ObjectKey, err = store.LogSegmentObjectKey(commit.Subject, commit.RetentionClass, second.Index)
	if err != nil {
		return err
	}
	bad.Segments = []store.Segment{second, {Index: 3, Offset: 5, Size: 1, Digest: strings.Repeat("c", 64)}}
	bad.Segments[1].ObjectKey, err = store.LogSegmentObjectKey(commit.Subject, commit.RetentionClass, 3)
	if err != nil {
		return err
	}
	if err := tenant.RecordSegments(ctx, bad); err == nil {
		return errors.New("gapped log batch committed")
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	request := blob.LogReadRequest{Subject: commit.Subject, Offset: 1}
	got, err := tenant.ReadLogSegments(ctx, commit.Actor, request)
	if err != nil || got.Complete || got.IncompleteReason != "" || !reflect.DeepEqual(got.Segments, []store.Segment{first}) {
		return fmt.Errorf("reopened open log=%+v: %v", got, err)
	}
	request.Offset = 3
	if got, err := tenant.ReadLogSegments(ctx, commit.Actor, request); err != nil || len(got.Segments) != 0 {
		return fmt.Errorf("failed batch left a readable suffix: %+v: %v", got, err)
	}
	commit.Segments, commit.Final, commit.IncompleteReason = []store.Segment{second}, true, "writer lost its final bytes"
	for range 2 {
		if err := tenant.RecordSegments(ctx, commit); err != nil {
			return err
		}
	}
	got, err = tenant.ReadLogSegments(ctx, commit.Actor, request)
	if err != nil || got.Complete || got.IncompleteReason != commit.IncompleteReason || !reflect.DeepEqual(got.Segments, []store.Segment{second}) {
		return fmt.Errorf("sealed incomplete log=%+v: %v", got, err)
	}
	bytes, err := tenant.StorageRetainedBytes(ctx)
	if err != nil || bytes != 5 {
		return fmt.Errorf("replayed log retained bytes=%d, want 5: %v", bytes, err)
	}
	var status, outcome string
	if err := s.DB().QueryRowContext(ctx, "SELECT status,outcome FROM nodes WHERE team=? AND run_id='logs' AND node_id='job'", store.DefaultTeam).Scan(&status, &outcome); err != nil {
		return err
	}
	if status != "running" || outcome != "" {
		return fmt.Errorf("incomplete log changed job result: %s/%s", status, outcome)
	}
	bad = commit
	bad.IncompleteReason = "different final commit"
	if err := tenant.RecordSegments(ctx, bad); err == nil {
		return errors.New("sealed log accepted a different final commit")
	}
	foreign := request
	foreign.Subject = commit.Subject
	attempt := *foreign.Subject.Attempt
	attempt.NodeID = "unrelated"
	foreign.Subject.Attempt = &attempt
	if _, err := tenant.ReadLogSegments(ctx, commit.Actor, foreign); err == nil {
		return errors.New("job log authority read another subject")
	}
	if err := checkStoreTerminalLogReads(ctx, s.DB(), tenant, commit, request); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", time.Now().UnixNano(), commit.Actor.CapabilityPrefix); err != nil {
		return err
	}
	if err := tenant.RecordSegments(ctx, commit); err == nil {
		return errors.New("revoked writer replayed a final seal")
	}
	if _, err := tenant.ReadLogSegments(ctx, commit.Actor, request); err == nil {
		return errors.New("revoked reader read persisted logs")
	}
	return checkStoreAbandonedLogBoundary(ctx, root)
}

func checkStoreTerminalLogReads(ctx context.Context, db *sql.DB, tenant *store.Tenant, commit store.SegmentCommit, request blob.LogReadRequest) error {
	finished := time.Now().Add(-2 * limits.LogSealGrace).UnixNano()
	if _, err := db.ExecContext(ctx, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team=? AND run_id='logs'", finished, store.DefaultTeam); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE team=? AND run_id='logs'", finished, store.DefaultTeam); err != nil {
		return err
	}
	if _, err := tenant.ReadLogSegments(ctx, commit.Actor, request); err == nil {
		return errors.New("terminal writer borrowed sealed replay to read beyond log grace")
	}
	if err := tenant.RecordSegments(ctx, commit); err != nil {
		return fmt.Errorf("identical final log replay: %w", err)
	}
	reader, err := seedStoreLogReader(ctx, db, commit.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	got, err := tenant.ReadLogSegments(ctx, reader, request)
	if err != nil || len(got.Segments) != 1 || got.IncompleteReason != commit.IncompleteReason {
		return fmt.Errorf("historical log read=%+v: %v", got, err)
	}
	return nil
}

func seedStoreLogReader(ctx context.Context, db *sql.DB, writer string) (store.Actor, error) {
	var body []byte
	if err := db.QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", writer).Scan(&body); err != nil {
		return store.Actor{}, err
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return store.Actor{}, err
	}
	c := &record.Value
	c.Prefix, c.Digest, c.Binding = "log-reader", sha256.Sum256([]byte("log-reader")), capability.Binding{Kind: capability.BindingNone}
	c.Grants = []capability.Grant{capability.LogsRead}
	c.Data = nil
	if err := record.Validate(); err != nil {
		return store.Actor{}, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return store.Actor{}, err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return store.Actor{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,created_at,created_by,repo_scope) VALUES (?,?,?,?,?,'controller',?,'delegate',1,?,'none',?,?,'logger',?)`, c.Prefix, hex.EncodeToString(c.Digest[:]), store.DefaultTeam, c.Subject.Kind, c.Subject.ID, grants, body, []byte(`[]`), c.IssuedAt.UnixNano(), []byte(`[]`)); err != nil {
		return store.Actor{}, err
	}
	return store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}}, nil
}

// safety: seeded authority isolates log persistence from claim issuance and byte transport.
func seedStoreLogBoundary(ctx context.Context, db *sql.DB) (store.SegmentCommit, error) {
	now := time.Now().UTC()
	c := capability.Capability{
		Digest: sha256.Sum256([]byte("log-boundary")), Prefix: "log-boundary", Team: string(store.DefaultTeam),
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "logger"}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.LogTail, capability.LogsRead}, Issuance: capability.Delegate,
		Binding:  capability.Binding{Kind: capability.BindingNode, RunID: "logs", NodeID: "job", Attempt: 1, Generation: 7, Holder: "logger"},
		IssuedAt: now, ReservedPools: []string{}, Repositories: []string{},
	}
	record := store.CapabilityRecord{Version: 1, Value: c}
	if err := record.Validate(); err != nil {
		return store.SegmentCommit{}, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return store.SegmentCommit{}, err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return store.SegmentCommit{}, err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,'logs',1,'fixture','running',?,'fixture',?,'gh:1','host','execute',?,'logger',?,'r30',30)`, []any{store.DefaultTeam, []byte(`{"version":1}`), now.UnixNano(), []byte(`[]`), now.UnixNano()}},
		{`INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,generation,attempt,lease_expires_at,spec_json,execution_json)
VALUES (?,'logs','job','running','logger',?,7,1,?,?,?)`, []any{store.DefaultTeam, c.Prefix, now.Add(time.Hour).UnixNano(), []byte(`{}`), []byte(`{}`)}},
		{`INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at)
VALUES (?,'logs','job',1,7,'logger',?,'runner','logger','remote',?)`, []any{store.DefaultTeam, c.Prefix, now.UnixNano()}},
		{`INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope)
VALUES (?,?,?,'machine','logger','controller',?,'delegate',1,?,'node','logs','job',7,'logger',?,?,'logger',?)`, []any{c.Prefix, hex.EncodeToString(c.Digest[:]), store.DefaultTeam, grants, body, []byte(`[]`), now.UnixNano(), []byte(`[]`)}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return store.SegmentCommit{}, err
		}
	}
	attempt := runspec.AttemptIdentity{Team: c.Team, RunID: "logs", NodeID: "job", Attempt: 1, Generation: 7}
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Holder: "logger", Generation: 7}
	return store.SegmentCommit{Subject: blob.LogSubject{Kind: blob.LogJobAttempt, Team: c.Team, RunID: "logs", Attempt: &attempt}, Actor: actor, RetentionClass: "r30"}, nil
}
