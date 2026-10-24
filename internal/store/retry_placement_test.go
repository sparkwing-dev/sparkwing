package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// RetryPlacementRegressions checks sealed writer placement in the supplied dialect.
func RetryPlacementRegressions(t *testing.T, open func(*testing.T) *Store) {
	t.Run("writer SQL parity", func(t *testing.T) { writerPrincipalSQLMatchesSealedWriter(t, open) })
	t.Run("personal machine", func(t *testing.T) { retryPersonalPlacementUsesSealedWriter(t, open) })
}

func writerPrincipalSQLMatchesSealedWriter(t *testing.T, open func(*testing.T) *Store) {
	s := open(t)
	writers := []runspec.Writer{"", "account", "schedule/nightly", "run/run-1", "actions", "other/account"}
	for _, kind := range []string{"account", "machine", "local"} {
		for _, id := range []string{"bob", "%", "/", ":", "%2F/%3A:%25", "élève/東京:%", "a'b\"c"} {
			writers = append(writers, runspec.PrincipalWriter(kind, id))
		}
	}
	for _, writer := range writers {
		for _, kind := range []string{"account", "machine", "local"} {
			t.Run(fmt.Sprintf("%s/%s", kind, writer), func(t *testing.T) {
				want := ""
				if principal := writer.Principal(); principal.Kind == kind {
					want = principal.ID
				}
				var got string
				if err := s.queryRow(t.Context(), `SELECT `+writerPrincipalSQL("writer", kind)+` FROM (SELECT CAST(? AS TEXT) AS writer) principal`, writer).Scan(&got); err != nil || got != want {
					t.Fatalf("SQL principal=%q, sealed writer principal=%q: %v", got, want, err)
				}
			})
		}
	}
}

func retryPersonalPlacementUsesSealedWriter(t *testing.T, open func(*testing.T) *Store) {
	for _, mode := range []string{"all", "failed"} {
		for _, queued := range []bool{false, true} {
			for _, account := range []string{"alice", "bob"} {
				t.Run(fmt.Sprintf("%s/queued=%v/%s", mode, queued, account), func(t *testing.T) {
					s := open(t)
					tenant := referenceWorld(t, s, "gh:701")
					alice := referenceMember(t, s, "alice", RoleEditor)
					original, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: alice.Authority, Request: runspec.Request{Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:701", Display: "https://github.com/acme/app"}, Ref: "refs/heads/main", SHA: referenceFeature}, Source: runspec.Source{Ref: referenceFeature, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git"}}}})
					if err != nil {
						t.Fatal(err)
					}
					source := original.RunID
					bob := referenceMember(t, s, "bob", RoleEditor)
					retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: bob.Authority, RetryMode: mode, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: source}}})
					if err != nil {
						t.Fatal(err)
					}
					s.testOnlyExec(t, `UPDATE runs SET status='failed',finished_at=? WHERE team='a' AND id=?`, s.now().UnixNano(), source)
					s.testOnlyExec(t, `UPDATE runs SET created_principal='bob',created_subject_kind='account' WHERE team='a' AND id=?`, retry.RunID)
					authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: account}
					machine, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:personal", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "personal"}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
					if err != nil {
						t.Fatal(err)
					}
					demand := UnitDemand{CapabilityPrefix: machine.Capability.Prefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(retry.RunID)}}
					if queued {
						demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitTrigger}
					}
					claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
					if account == "alice" {
						if err != nil || claim.RunID != retry.RunID {
							t.Fatalf("source writer's machine claim=%+v: %v", claim, err)
						}
						principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
						if err != nil {
							t.Fatal(err)
						}
						owned, err := tenant.OwnerMachineClaimant(t.Context(), principal, retry.RunID)
						if err != nil || !owned {
							t.Fatalf("source writer's personal award not owned: %v %v", owned, err)
						}
					} else if err == nil {
						t.Fatal("retrier's machine claimed the source writer's code")
					}
				})
			}
		}
	}
}
