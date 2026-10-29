package store

import (
	"fmt"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestOperatorSettingsRevalidateAuthorityProperty(t *testing.T) {
	property := func(input uint16) bool {
		ok := true
		for phase := range 11 {
			t.Run(fmt.Sprintf("%d/%d", input, phase), func(t *testing.T) {
				s := authorityStore(t)
				root := authorityRoot("ops", "")
				root.Subject.Kind = capability.SubjectService
				root.Grants = []capability.Grant{capability.OperatorLimits, capability.OperatorObjectStore}
				a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Capability: root}}
				past := time.Now().Add(-time.Hour)
				switch phase {
				case 1:
					root.RevokedAt = &past
				case 2:
					root.ExpiresAt = &past
				case 3:
					root.Grants = nil
				case 4:
					root.Team = "a"
				case 5:
					root.Team = "a"
					root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Repository: "acme/repo", Ref: "main", SHA: "commit", WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
				case 6:
					root.Team = "a"
					root.Subject.Kind = capability.SubjectAccount
				case 7:
					a.Principal.Authority.ID = "missing"
				case 8:
					root.Grants = []capability.Grant{capability.OperatorLimits}
				case 9:
					root.Grants = []capability.Grant{capability.OperatorObjectStore}
				case 10:
					root.Grants = []capability.Grant{capability.OperatorTokens}
				}
				authorityPersist(t, s, root)
				ctx, op := t.Context(), s.AsOperator()
				v := int64(input) + 1
				gate, err := s.AsOperator().SignUpGate(ctx)
				if err != nil {
					t.Fatal(err)
				}
				mode, hourly := "waitlist", int(v)
				writes := []func() error{
					func() error {
						_, err := op.WriteComputeLimits(ctx, a, map[string]int64{ComputeLimitGlobalRunners: v})
						return err
					},
					func() error {
						return op.WriteSignUpSettings(ctx, a, SignUpSettingsChange{Mode: &mode, HourlyLimit: &hourly})
					},
					func() error { return op.WriteStorageSettings(ctx, a, StorageSettings{RunRetentionDays: v}) },
					func() error { _, err := op.WriteStorageQuota(ctx, a, StorageQuota{MaxBytesPerRun: v}); return err },
					func() error { _, err := op.WriteStorageAllowance(ctx, a, v); return err },
				}
				for i, write := range writes {
					if i >= 3 {
						a.Target.Team = "a"
					}
					err := write()
					want := phase == 0 || phase == 8 && i < 2 || phase == 9 && i >= 2
					if (err == nil) != want {
						t.Errorf("write %d phase %d: %v", i, phase, err)
						ok = false
					}
				}
				limits, err := op.ComputeLimits(ctx)
				if err != nil {
					t.Fatal(err)
				}
				settings, err := op.StorageSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				quota, err := (&Tenant{s: s, team: "a"}).StorageQuota(ctx)
				if err != nil {
					t.Fatal(err)
				}
				after, err := s.AsOperator().SignUpGate(ctx)
				if err != nil {
					t.Fatal(err)
				}
				wantLimit, wantStorage := int64(0), int64(0)
				if phase == 0 || phase == 8 {
					wantLimit = v
				}
				if phase == 0 || phase == 9 {
					wantStorage = v
				}
				if limits.GlobalRunners != wantLimit || settings.RunRetentionDays != wantStorage || quota.MaxBytesPerRun != wantStorage || quota.AllowanceBytes != wantStorage {
					t.Errorf("committed values: %+v %+v %+v", limits, settings, quota)
					ok = false
				}
				if wantLimit > 0 {
					if after.Limits.HourlyLimit != hourly || after.SetBy != "ops" || after.Mode != SignUpWaitlist {
						t.Errorf("committed gate: %+v", after)
						ok = false
					}
				} else if after != gate {
					t.Errorf("refused authority changed gate: %+v", after)
					ok = false
				}
			})
		}
		return ok
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 12})); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorSignUpBatchRollsBackInvalidSibling(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("ops", "")
	root.Subject.Kind = capability.SubjectOperator
	root.Grants = []capability.Grant{capability.OperatorLimits}
	authorityPersist(t, s, root)
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}}}
	before, err := s.AsOperator().SignUpGate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mode, good, bad := "waitlist", 99, -1
	if err := s.AsOperator().WriteSignUpSettings(t.Context(), a, SignUpSettingsChange{Mode: &mode, HourlyLimit: &good, DailyLimit: &bad}); err == nil {
		t.Fatal("invalid sibling committed")
	}
	after, err := s.AsOperator().SignUpGate(t.Context())
	if err != nil || after != before {
		t.Fatalf("changed gate: %+v %v", after, err)
	}
}

func TestOperatorSettingsConsoleProofProperty(t *testing.T) {
	property := func(input uint16, phase uint8) bool {
		s := authorityStore(t)
		actor := authorityMember(t, s, "owner", RoleOwner)
		member, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
		if err != nil {
			t.Fatal(err)
		}
		s.consolePolicy = func(p capability.Principal, _ time.Time) bool {
			return p.DirectAccountSession() && p.Capability.Subject.ID == "owner"
		}
		member.Class = capability.CredentialConsole
		member.Capability.Grants = []capability.Grant{capability.OperatorConsole}
		a := capability.Authorized{Principal: member}
		switch phase % 6 {
		case 1:
			s.consolePolicy = func(capability.Principal, time.Time) bool { return false }
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash='owner'`)
		case 3:
			parent := authorityRoot("derived", "a")
			parent.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
			authorityPersist(t, s, parent)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET capability_prefix='derived' WHERE hash='owner'`)
		case 4:
			a.Principal.Authority.ID = "missing"
		case 5:
			calls := 0
			s.consolePolicy = func(p capability.Principal, _ time.Time) bool {
				calls++
				return calls == 1 && p.DirectAccountSession()
			}
		}
		v := int64(input) + 1
		_, err = s.AsOperator().WriteComputeLimits(t.Context(), a, map[string]int64{ComputeLimitGlobalRunners: v})
		if (err == nil) != (phase%6 == 0) {
			return false
		}
		limits, err := s.AsOperator().ComputeLimits(t.Context())
		want := int64(0)
		if phase%6 == 0 {
			want = v
		}
		return err == nil && limits.GlobalRunners == want
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 48})); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentBindingAllowsOnlyUnboundTeamSelectionProperty(t *testing.T) {
	property := func(input uint16) bool {
		s := authorityStore(t)
		root := authorityRoot("ops", "")
		root.Subject.Kind = capability.SubjectOperator
		root.Grants = []capability.Grant{capability.OperatorObjectStore}
		authorityPersist(t, s, root)
		p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix})
		if err != nil {
			t.Fatal(err)
		}
		target := capability.Target{Team: fmt.Sprintf("target-%d", input)}
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindOperatorSettings, target); err != nil {
			return false
		}
		target.RunID = fmt.Sprintf("run-%d", input)
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindOperatorSettings, target); err == nil {
			return false
		}
		target.RunID, target.NodeID = "", fmt.Sprintf("job-%d", input)
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindOperatorSettings, target); err == nil {
			return false
		}
		tenant := authorityRoot("tenant", "a")
		tenant.Grants = root.Grants
		authorityPersist(t, s, tenant)
		p, err = s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: tenant.Prefix})
		if err != nil {
			t.Fatal(err)
		}
		target.NodeID = ""
		return s.AsOperator().CheckBinding(t.Context(), p, capability.BindOperatorSettings, target) != nil
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32})); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentBindNoneRefusesCrossTeamProperty(t *testing.T) {
	property := func(value uint16) bool {
		s := authorityStore(t)
		root := authorityRoot("ops", "")
		root.Subject.Kind = capability.SubjectOperator
		root.Grants = []capability.Grant{capability.OperatorObjectStore}
		authorityPersist(t, s, root)
		p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix})
		if err != nil {
			t.Fatal(err)
		}
		return s.AsOperator().CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: fmt.Sprintf("target-%d", value)}) != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}
