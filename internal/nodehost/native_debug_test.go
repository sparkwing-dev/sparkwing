package nodehost

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeOwnerPersistsDebugPauseUntilReleased(t *testing.T) {
	for _, reason := range []string{store.PauseReasonBefore, store.PauseReasonAfter, store.PauseReasonOnFailure} {
		for _, release := range []string{"manual", "timeout", "cancelled"} {
			t.Run(reason+"/"+release, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg, _ := nativeConfigFixture(t)
					cfg.Source.Base.Local = false
					options := debugOptions(reason)
					options.PauseTimeout.Set, options.PauseTimeout.Value, options.PauseTimeout.Origin = true, time.Second, options.Pause.Origin
					f := newStoreHostFixtureOn(t, openStore(t, teststore.WithClock(time.Now)), nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, options)
					cfg.Store = f.State
					cfg.Principal.Capability.Binding.RunID = f.Input.RunID
					cfg.Refresh = func(context.Context) (capability.Principal, error) { return cfg.Principal, nil }
					services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return &processListener{closed: make(chan struct{})}, nil })
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := services.Close(); err != nil {
							t.Error(err)
						}
					}()
					f.Host.Debug = services.Config.Owner.Debug
					f.Host.Executor = ownerNodeFunc(func(ctx context.Context, in NodeInput) error {
						outcome := runstate.NodeSuccess
						if reason == store.PauseReasonOnFailure {
							outcome = runstate.NodeFailed
						}
						return f.report(ctx, in, outcome)
					})
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan error, 1)
					go func() { _, err := f.Host.Run(ctx, f.Input); done <- err }()
					synctest.Wait()
					pause, err := f.Tenant.GetActiveDebugPause(t.Context(), f.Input.RunID, "build")
					if err != nil || pause.Reason != reason {
						cancel()
						hostErr := <-done
						t.Fatalf("debug pause: %+v, %v; host: %v", pause, err, hostErr)
					}
					row, err := f.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "build"})
					if err != nil {
						t.Fatal(err)
					}
					stale := f.Actor
					stale.Owner.Generation++
					if _, err := f.Tenant.OwnerDebugPause(t.Context(), row.Ref, int64(row.Generation), stale, reason); err == nil {
						t.Fatal("stale owner changed the pause")
					}
					switch release {
					case "manual":
						_, member, err := teststore.ProvisionCredential(t.Context(), f.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "debugger"}, Grants: []capability.Grant{capability.RunsControl}, Binding: capability.Binding{Kind: capability.BindingNone}})
						if err != nil {
							t.Fatal(err)
						}
						principal, err := f.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: member.Prefix})
						if err != nil {
							t.Fatal(err)
						}
						for range 2 {
							if err := f.Tenant.ResumeAuthorizedDebugPause(t.Context(), capability.Authorized{Principal: principal, Target: capability.Target{Team: string(store.DefaultTeam), RunID: f.Input.RunID, NodeID: "build"}}); err != nil {
								t.Fatal(err)
							}
						}
					case "cancelled":
						if _, err := f.Tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(f.Input.RunID), To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: f.Control}); err != nil {
							t.Fatal(err)
						}
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					history, err := f.Tenant.ListDebugPauses(t.Context(), f.Input.RunID)
					if err != nil || len(history) != 1 || history[0].ReleasedAt == nil {
						t.Fatalf("released pause history: %+v, %v", history, err)
					}
					if history[0].ReleaseKind != release {
						t.Fatalf("release kind %q, want %q", history[0].ReleaseKind, release)
					}
				})
			})
		}
	}
}
