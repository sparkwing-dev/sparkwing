package acceptance_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

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
VALUES (?,'logs',1,'fixture','running',?,'fixture',?,'gh:1','host','execute',?,'logger',?,'r30',30)`, []any{store.DefaultTeam, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"fixture","writer":"account/fixture"}}`), now.UnixNano(), []byte(`[]`), now.UnixNano()}},
		{`INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,generation,attempt,lease_expires_at,started_at,spec_json,execution_json)
VALUES (?,'logs','job','running','logger',?,7,1,?,?,?,?)`, []any{store.DefaultTeam, c.Prefix, now.Add(time.Hour).UnixNano(), now.UnixNano(), []byte(`{}`), []byte(`{}`)}},
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
