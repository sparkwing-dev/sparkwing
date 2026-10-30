package nodehost

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeForegroundOwnerPersistsDebugPauseUntilReleased(t *testing.T) {
	for _, reason := range []string{store.PauseReasonBefore, store.PauseReasonAfter, store.PauseReasonOnFailure} {
		for _, release := range []string{"manual", "timeout", "cancelled"} {
			t.Run(reason+"/"+release, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg, _ := nativeConfigFixture(t)
					cfg.Source.Base.Local = false
					options := debugOptions(reason)
					options.PauseTimeout.Set, options.PauseTimeout.Value, options.PauseTimeout.Origin = true, time.Second, options.Pause.Origin
					request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Options: options}
					f := newStoreHostFixtureForRequest(t, openStore(t, teststore.WithClock(time.Now)), nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, request)
					objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
					if err != nil {
						t.Fatal(err)
					}
					defer objects.Close()
					server := controller.New(f.Store, nil).WithDataObjects(objects)
					transportOptions := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: nativeDebugTransport{server.Handler()}}, Bearer: func(context.Context) (string, error) { return string(f.OwnerBearer), nil }}
					wire, err := client.NewEngineClient(transportOptions)
					if err != nil {
						t.Fatal(err)
					}
					private, err := client.NewLocalHostClient(transportOptions, f.Input.RunID)
					if err != nil {
						t.Fatal(err)
					}
					defer private.Close()
					remote, err := NewRemoteHostStore(wire, private, "owner", func() bool { return false })
					if err != nil {
						t.Fatal(err)
					}
					remote.Finish = private.FinishRunAndUnit
					cfg.Store, cfg.Parent, cfg.Bindings = remote, private, private
					cfg.Principal, err = private.Principal(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					cfg.Refresh = private.Principal
					cfg = WithClient(cfg, private)
					services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return &processListener{closed: make(chan struct{})}, nil })
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := services.Close(); err != nil {
							t.Error(err)
						}
					}()
					host, _, _, err := NewNativeHost(services.Config)
					if err != nil {
						t.Fatal(err)
					}
					host.Acquire, host.Renew = f.Host.Acquire, f.Host.Renew
					adopt := host.AdoptClaim
					host.AdoptClaim = func(ctx context.Context, claim runstate.ReconciledNode) error {
						if err := private.AdoptClaim(ctx, claim); err != nil {
							return err
						}
						return adopt(ctx, claim)
					}
					f.Host = *host
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
					for _, invalid := range []struct {
						ref        runspec.NodeRef
						generation int64
						reason     string
					}{
						{row.Ref, int64(row.Generation) + 1, reason},
						{row.Ref, int64(row.Generation), "unselected"},
						{runspec.NodeRef{RunID: "foreign", NodeID: "build"}, int64(row.Generation), reason},
					} {
						if _, err := remote.OwnerDebugPause(t.Context(), invalid.ref, invalid.generation, f.Actor, invalid.reason); err == nil {
							t.Fatal("invalid boundary changed the pause")
						}
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

type nativeDebugTransport struct{ handler http.Handler }

func (tr nativeDebugTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	tr.handler.ServeHTTP(response, r)
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	return response.Result(), nil
}
