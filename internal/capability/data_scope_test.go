package capability_test

import (
	"context"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type dataCheck func(context.Context, capability.Authorized, capability.DataTarget) error

func (f dataCheck) CheckDataAccess(ctx context.Context, a capability.Authorized, target capability.DataTarget) error {
	return f(ctx, a, target)
}

func TestDataRefScope(t *testing.T) {
	p := workPrincipal()
	p.Capability.Grants = append(p.Capability.Grants, capability.DataWrite)
	p.Capability.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, WriteRef: "manual:main", ReadRefs: []string{"manual:main", "main"}}
	a := capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "team"}, Access: capability.AccessRule{Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveSensitive}}
	target := capability.DataTarget{Operation: capability.DataRecord, RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, RecordKind: "memo", RecordScope: "manual:main", RecordName: "job-hash"}
	calls := 0
	edges := dataCheck(func(context.Context, capability.Authorized, capability.DataTarget) error { calls++; return nil })
	if err := capability.AuthorizeData(context.Background(), edges, a, target); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("receipt/edge owner not consulted")
	}
	for _, tc := range []struct {
		name   string
		change func(*capability.DataTarget)
	}{
		{"foreign run", func(x *capability.DataTarget) { x.RunID = "foreign" }},
		{"sibling upload", func(x *capability.DataTarget) { x.NodeID = "sibling" }},
		{"stale generation", func(x *capability.DataTarget) { x.Generation-- }},
		{"old attempt", func(x *capability.DataTarget) { x.Attempt++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := target
			tc.change(&x)
			requireCode(t, capability.AuthorizeData(context.Background(), edges, a, x), apierr.Forbidden)
		})
	}
	if calls != 1 {
		t.Fatal("scope failure reached edge owner")
	}
	denied := dataCheck(func(context.Context, capability.Authorized, capability.DataTarget) error {
		return &apierr.Error{Code: apierr.Forbidden, Message: "digest is not readable"}
	})
	target.Operation, target.Digest = capability.DataDownload, "digest"
	a.Access.Grants = []capability.Grant{capability.DataRead}
	requireCode(t, capability.AuthorizeData(context.Background(), denied, a, target), apierr.Forbidden)
	p.Capability.Data.Use = capability.DataRemoteCache
	p.Capability.Data.VerifiedDefaultOnly = true
	a.Principal = p
	target.Operation = capability.DataRecord
	a.Access.Grants = []capability.Grant{capability.DataWrite}
	requireCode(t, capability.AuthorizeData(context.Background(), edges, a, target), apierr.Forbidden)
}

func TestSourceReleaseReachesSharedRunnersAndCloudOnly(t *testing.T) {
	p := workPrincipal()
	p.Capability.Repositories = []string{"github.com/team/repo"}
	a := capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "team", RunID: "run"}, Access: capability.AccessRule{Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindSource, Liveness: capability.LiveSensitive}}
	request := capability.SourceReleaseRequest{Authorization: a, Executor: capability.SourceExecutor{Team: "team", MachineID: "machine", OwnerAccountID: "owner", AuthorizedRepositories: p.Capability.Repositories}, RepositoryKey: p.Capability.Repositories[0], CredentialID: "git", Host: "github.com"}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	requireCode(t, sourceError(request, now), apierr.Forbidden)
	request.Executor.Shared = true
	audit, err := capability.AuthorizeSourceRelease(request, now)
	if err != nil || audit.Team != "team" || audit.RunID != "run" || audit.CapabilityPrefix != "work" || audit.ReleasedAt != now || audit.ID == "" {
		t.Fatalf("audit=%+v, err=%v", audit, err)
	}
	request.Executor.Shared, request.Executor.Cloud = false, true
	if err := sourceError(request, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*capability.SourceReleaseRequest)
	}{
		{"wrong team", func(r *capability.SourceReleaseRequest) { r.Executor.Team = "other" }},
		{"wrong repo", func(r *capability.SourceReleaseRequest) { r.RepositoryKey = "github.com/other/repo" }},
		{"executor repo", func(r *capability.SourceReleaseRequest) { r.Executor.AuthorizedRepositories = nil }},
		{"ssh confirmation", func(r *capability.SourceReleaseRequest) { r.SSH = true }},
		{"reporting authority", func(r *capability.SourceReleaseRequest) { r.Authorization.Access.Liveness = capability.LiveReporting }},
		{"claimless", func(r *capability.SourceReleaseRequest) { r.Authorization.Principal.Capability.UnitID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) { r := request; tc.change(&r); requireCode(t, sourceError(r, now), apierr.Forbidden) })
	}
}

func TestSourceReleaseNeverReachesAPersonalMachineProperty(t *testing.T) {
	p := workPrincipal()
	p.Capability.Repositories = []string{"github.com/team/repo"}
	a := capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "team", RunID: "run"}, Access: capability.AccessRule{Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindSource, Liveness: capability.LiveSensitive}}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	property := func(githubApp, ssh, confirmed bool, owner string) bool {
		request := capability.SourceReleaseRequest{Authorization: a, Executor: capability.SourceExecutor{Team: "team", MachineID: "laptop", OwnerAccountID: owner, AuthorizedRepositories: p.Capability.Repositories}, RepositoryKey: p.Capability.Repositories[0], CredentialID: "git", Host: "github.com", GitHubApp: githubApp, SSH: ssh, SSHHostConfirmed: confirmed}
		audit, err := capability.AuthorizeSourceRelease(request, now)
		var failure *apierr.Error
		return audit.ID == "" && errors.As(err, &failure) && failure.Details["reason"] == apierr.ReasonPersonalMachine
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}

func sourceError(r capability.SourceReleaseRequest, now time.Time) error {
	_, err := capability.AuthorizeSourceRelease(r, now)
	return err
}
