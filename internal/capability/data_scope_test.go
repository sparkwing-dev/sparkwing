package capability

import (
	"context"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type dataCheck func(context.Context, Authorized, DataTarget) error

func (f dataCheck) CheckDataAccess(ctx context.Context, a Authorized, target DataTarget) error {
	return f(ctx, a, target)
}

func TestDataRefScope(t *testing.T) {
	p := workPrincipal()
	p.Capability.Grants = append(p.Capability.Grants, DataWrite)
	p.Capability.Data = &DataScope{Use: DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, WriteRef: "manual:main", ReadRefs: []string{"manual:main", "main"}}
	a := Authorized{Principal: p, Binding: p.Capability.Binding, Target: Target{Team: "team"}, Access: AccessRule{Grants: []Grant{DataWrite}, Binding: BindData, Liveness: LiveSensitive}}
	target := DataTarget{Operation: DataRecord, RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, RecordKind: "memo", RecordScope: "manual:main", RecordName: "job-hash"}
	calls := 0
	edges := dataCheck(func(context.Context, Authorized, DataTarget) error { calls++; return nil })
	if err := AuthorizeData(context.Background(), edges, a, target); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("receipt/edge owner not consulted")
	}
	for _, tc := range []struct {
		name   string
		change func(*DataTarget)
	}{
		{"foreign run", func(x *DataTarget) { x.RunID = "foreign" }},
		{"sibling upload", func(x *DataTarget) { x.NodeID = "sibling" }},
		{"stale generation", func(x *DataTarget) { x.Generation-- }},
		{"old attempt", func(x *DataTarget) { x.Attempt++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := target
			tc.change(&x)
			requireCode(t, AuthorizeData(context.Background(), edges, a, x), apierr.Forbidden)
		})
	}
	if calls != 1 {
		t.Fatal("scope failure reached edge owner")
	}
	denied := dataCheck(func(context.Context, Authorized, DataTarget) error {
		return &apierr.Error{Code: apierr.Forbidden, Message: "digest is not readable"}
	})
	target.Operation, target.Digest = DataDownload, "digest"
	a.Access.Grants = []Grant{DataRead}
	requireCode(t, AuthorizeData(context.Background(), denied, a, target), apierr.Forbidden)
	p.Capability.Data.Use = DataRemoteCache
	p.Capability.Data.VerifiedDefaultOnly = true
	a.Principal = p
	target.Operation = DataRecord
	a.Access.Grants = []Grant{DataWrite}
	requireCode(t, AuthorizeData(context.Background(), edges, a, target), apierr.Forbidden)
}

func TestSourceReleaseReachesSharedRunnersAndCloudOnly(t *testing.T) {
	p := workPrincipal()
	p.Capability.Repositories = []string{"github.com/team/repo"}
	a := Authorized{Principal: p, Binding: p.Capability.Binding, Target: Target{Team: "team", RunID: "run"}, Access: AccessRule{Grants: []Grant{SourceRead}, Binding: BindSource, Liveness: LiveSensitive}}
	request := SourceReleaseRequest{Authorization: a, Executor: SourceExecutor{Team: "team", MachineID: "machine", OwnerAccountID: "owner", AuthorizedRepositories: p.Capability.Repositories}, RepositoryKey: p.Capability.Repositories[0], CredentialID: "git", Host: "github.com"}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	requireCode(t, sourceError(request, now), apierr.Forbidden)
	request.Executor.Shared = true
	audit, err := AuthorizeSourceRelease(request, now)
	if err != nil || audit.Team != "team" || audit.RunID != "run" || audit.CapabilityPrefix != "work" || audit.ReleasedAt != now || audit.ID == "" {
		t.Fatalf("audit=%+v, err=%v", audit, err)
	}
	request.Executor.Shared, request.Executor.Cloud = false, true
	if err := sourceError(request, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*SourceReleaseRequest)
	}{
		{"wrong team", func(r *SourceReleaseRequest) { r.Executor.Team = "other" }},
		{"wrong repo", func(r *SourceReleaseRequest) { r.RepositoryKey = "github.com/other/repo" }},
		{"executor repo", func(r *SourceReleaseRequest) { r.Executor.AuthorizedRepositories = nil }},
		{"ssh confirmation", func(r *SourceReleaseRequest) { r.SSH = true }},
		{"reporting authority", func(r *SourceReleaseRequest) { r.Authorization.Access.Liveness = LiveReporting }},
		{"claimless", func(r *SourceReleaseRequest) { r.Authorization.Principal.Capability.UnitID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) { r := request; tc.change(&r); requireCode(t, sourceError(r, now), apierr.Forbidden) })
	}
}

func TestSourceReleaseNeverReachesAPersonalMachineProperty(t *testing.T) {
	p := workPrincipal()
	p.Capability.Repositories = []string{"github.com/team/repo"}
	a := Authorized{Principal: p, Binding: p.Capability.Binding, Target: Target{Team: "team", RunID: "run"}, Access: AccessRule{Grants: []Grant{SourceRead}, Binding: BindSource, Liveness: LiveSensitive}}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	property := func(githubApp, ssh, confirmed bool, owner string) bool {
		request := SourceReleaseRequest{Authorization: a, Executor: SourceExecutor{Team: "team", MachineID: "laptop", OwnerAccountID: owner, AuthorizedRepositories: p.Capability.Repositories}, RepositoryKey: p.Capability.Repositories[0], CredentialID: "git", Host: "github.com", GitHubApp: githubApp, SSH: ssh, SSHHostConfirmed: confirmed}
		audit, err := AuthorizeSourceRelease(request, now)
		var failure *apierr.Error
		return audit.ID == "" && errors.As(err, &failure) && failure.Details["reason"] == apierr.ReasonPersonalMachine
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}

func sourceError(r SourceReleaseRequest, now time.Time) error {
	_, err := AuthorizeSourceRelease(r, now)
	return err
}
