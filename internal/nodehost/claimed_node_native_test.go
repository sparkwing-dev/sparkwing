//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type claimEngine struct{ resolved engine.Resolved }

func (r claimEngine) Resolve(context.Context, engineid.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("accepted execution must ensure its sealed engine")
}

func (r claimEngine) Ensure(_ context.Context, identity engineid.Identity, target releaseasset.Target) (engine.Resolved, error) {
	if identity != r.resolved.Identity || target != r.resolved.Target {
		return engine.Resolved{}, errors.New("source changed the selected engine")
	}
	return r.resolved, nil
}

func TestClaimedNodeHostAcquiresSourceAndJoinsNativeSDK(t *testing.T) {
	for _, outcome := range []string{"success", "lease_lost", "funding_refused", "controller_lost"} {
		t.Run(outcome, func(t *testing.T) {
			lost := outcome != "success"
			input, s, host, _ := claimedNodeFixture(t)
			root, sha := hostSourceCheckout(t)
			s.run.Spec.Request.Request.Source.Location = runspec.SourceLocation{LocalCheckout: root, PipelineDir: "."}
			s.run.Spec.Request.Request.Git.SHA = sha
			snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			s.run.Spec.Request.Request.Env, err = snapshots.Capture(t.Context(), []string{"PATH=" + os.Getenv("PATH")})
			if err != nil {
				t.Fatal(err)
			}
			host.Snapshots = snapshots
			s.run.Spec.Resolved.Entrypoint = "."
			input.Accepted.Spec, err = json.Marshal(s.run.Spec)
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			resolved := input.Engine
			resolved.Executable, resolved.Target = exe, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
			mode := "claimed_node"
			if lost {
				mode = "claimed_node_hold"
			}
			host.Acquire = nil
			installed := false
			host.Source = source.Options{
				Engines: claimEngine{resolved: resolved}, Target: resolved.Target, SDK: engineid.GoSDK,
				Base: procenv.Base{Local: true, Captured: []string{"PATH=/daemon/bin"}}, BuildEnvironment: sourcetest.BuildEnvironment,
				ReadPin: func(string, engineid.SDKKind) (engineid.Pin, error) { return resolved.Identity.Pin, nil },
				InstallSDK: func(_ context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
					directory, env := filepath.Join(request.Directory, request.PipelineDir), request.Environment
					installed = true
					if !reflect.DeepEqual(got, resolved) || directory != root || len(env) != 1 || !strings.HasPrefix(env[0], "PATH=") {
						t.Error("installer lost sealed engine, source or base environment")
					}
					return engine.SDKCommand{Directory: directory, Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", mode}}, nil
				},
			}
			testCtx, deadline := context.WithTimeout(t.Context(), 10*time.Second)
			defer deadline()
			ctx, cancel := context.WithCancelCause(testCtx)
			defer cancel(nil)
			var session *protocolSession
			var entered atomic.Bool
			revoked, cleaned := false, false
			cfg := ProtocolSessionConfig{
				Environments: sdkTestEnvironment(s), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil },
				RunCleanups: func(context.Context, CleanupScope) error { cleaned = true; return nil },
				Log: func(_ context.Context, log nodeproto.FrameLog) error {
					if log.Message == "entered" {
						entered.Store(true)
						if outcome == "lease_lost" {
							cancel(lease.ErrLeaseLost)
						}
					}
					return nil
				},
			}
			deps := host.Executor.(*nodeExecutor).deps
			authorities, parent, parentLookup, delegation, services := processAuthorityFixture(t)
			t.Cleanup(func() { _ = authorities.Close() })
			var issuedLookup capability.CredentialLookup
			deps.Bind = func(ctx context.Context, input NodeInput) (ProcessGrant, error) {
				delegation.Binding = capability.Binding{Kind: capability.BindingNode, RunID: input.Accepted.RunID, NodeID: input.NodeID, Holder: input.Authority.Holder, Generation: int64(input.Generation), Attempt: int64(input.Attempt)}
				parent.snapshot.Authorities[0].Principal.Capability.Binding = delegation.Binding
				parent.snapshot.Binding.Current = delegation.Binding
				services.RunID, services.Job = input.Accepted.RunID, input.Accepted.Plan.Jobs[0]
				issued, revoke, err := authorities.delegate(ctx, parentLookup, delegation, services)
				if err != nil {
					return ProcessGrant{}, err
				}
				issuedLookup = processLookup(issued)
				cfg.RunCleanups = func(ctx context.Context, scope CleanupScope) error {
					if _, err := authorities.ReadAuthentication(ctx, issuedLookup, capability.RequestEvidence{}); err == nil {
						t.Error("cleanup began with live process authority")
					}
					cleaned = true
					return nil
				}
				return ProcessGrant{Binding: nodeproto.ProcessBinding{RunID: input.Accepted.RunID, JobID: input.NodeID, Routes: nodeproto.ProcessRoutes{URL: "http://host", Bearer: issued.Raw}}, Revoke: func() error {
					revoked = true
					return revoke()
				}}, nil
			}
			deps.Masker = host.Masker
			deps.EnsureEngine = func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				return resolved, nil
			}
			deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := cfg.startNode(ctx, input, activity)
				if process != nil {
					session = process.(*protocolSession)
				}
				return process, err
			}
			host.Executor, err = NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "funding_refused" || outcome == "controller_lost" {
				host.Lease = lease.Keeper{Interval: 10 * time.Millisecond, Lease: time.Minute}
				var bound time.Time
				s.renew = func(context.Context) (runstate.LeaseBeat, error) {
					if !entered.Load() {
						return runstate.LeaseBeat{}, nil
					}
					if outcome == "funding_refused" {
						return runstate.LeaseBeat{CancelRequested: true, CancelReason: "out of credits", ExpiresAt: time.Now().Add(time.Second)}, nil
					}
					if bound.IsZero() {
						bound = time.Now().Add(100 * time.Millisecond)
						return runstate.LeaseBeat{ExpiresAt: bound}, nil
					}
					return runstate.LeaseBeat{}, &apierr.Error{Code: apierr.Unavailable, Message: "controller unreachable"}
				}
			}
			err = host.Execute(ctx, input)
			var cancelled *lease.CancelError
			switch outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "lease_lost":
				if !errors.Is(err, lease.ErrLeaseLost) {
					t.Fatalf("execute = %v", err)
				}
			case "funding_refused":
				if !errors.As(err, &cancelled) || cancelled.Reason != "out of credits" {
					t.Fatalf("execute = %v", err)
				}
			case "controller_lost":
				if !errors.Is(err, lease.ErrSilent) {
					t.Fatalf("execute = %v", err)
				}
			}
			if !installed || session == nil || session.cmd.ProcessState == nil || !revoked || !cleaned {
				t.Fatalf("installed=%t session=%v revoked=%t cleaned=%t", installed, session, revoked, cleaned)
			}
			if _, err := authorities.ReadAuthentication(t.Context(), issuedLookup, capability.RequestEvidence{}); err == nil {
				t.Fatal("claim host returned with a live process bearer")
			}
			if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
				t.Fatal("host removed the user's checkout:", err)
			}
			if outcome == "funding_refused" {
				if len(s.changes) != 2 || s.changes[1].Extra.Result.Outcome != runstate.NodeCancelled {
					t.Fatalf("funding cancellation transitions = %+v", s.changes)
				}
			} else if lost {
				for _, change := range s.changes {
					if change.Cause == runstate.CauseReport {
						t.Fatal("lost authority reported a terminal result")
					}
				}
			} else if len(s.changes) != 2 || s.changes[1].Extra.Result.Outcome != runstate.NodeSuccess {
				t.Fatalf("transitions = %+v", s.changes)
			}
		})
	}
}

func hostSourceCheckout(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "source"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	cmd := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "HEAD")
	sha, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(sha))
}
