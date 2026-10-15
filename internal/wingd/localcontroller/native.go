package localcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NativeServices binds the elected local store to shared source, process and host services.
func NativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding, sourceOptions source.Options) (_ runners.NativeServices, err error) {
	if owned == nil || binding.Tenant == nil || binding.Refresh == nil || binding.Principal.Capability.Team != string(binding.Tenant.Team()) || binding.Principal.Capability.Binding.RunID == "" {
		return runners.NativeServices{}, errors.New("local native host requires its authenticated tenant and run")
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: binding.Principal.Capability.Digest}
	current := func(ctx context.Context) (capability.Principal, error) {
		principal, err := binding.Refresh(ctx)
		if err == nil && principal.Authority != binding.Principal.Authority {
			err = errors.New("local host authority changed")
		}
		return principal, err
	}
	if _, err := current(ctx); err != nil {
		return runners.NativeServices{}, err
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		return runners.NativeServices{}, err
	}
	run, err := binding.Tenant.ReadSnapshot(ctx, binding.Principal.Capability.Binding.RunID)
	if err != nil {
		return runners.NativeServices{}, err
	}
	captured, err := snapshots.Load(ctx, run.Run.Request.Request.Env)
	if err != nil {
		return runners.NativeServices{}, err
	}
	masker := sourceOptions.Base.Masker
	if masker == nil {
		masker = secrets.NewMasker()
	}
	sourceOptions.Base.Local, sourceOptions.Base.Captured, sourceOptions.Base.Masker = true, captured, masker
	if run.Run.Spec != nil {
		sourceOptions.SDK = run.Run.Spec.Resolved.Engine.Pin.SDK
	}
	authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: owned, Bindings: owned})
	if err != nil {
		return runners.NativeServices{}, err
	}
	closeResources := authorities.Close
	defer func() {
		if err != nil {
			err = errors.Join(err, closeResources())
		}
	}()
	masks := nodehost.NewMaskRegistry(masker)
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return runners.NativeServices{}, err
	}
	server := &http.Server{Handler: router, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan error, 1)
	var serveOnce sync.Once
	var closeOnce sync.Once
	var closeErr error
	closeResources = func() error {
		closeOnce.Do(func() {
			serveOnce.Do(func() { serveDone <- listener.Close() })
			closeErr = errors.Join(authorities.Close(), server.Close())
			if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
				closeErr = errors.Join(closeErr, err)
			}
		})
		return closeErr
	}
	processes := &nodehost.NativeProcesses{Authorities: authorities, Masks: masks, URL: "http://" + listener.Addr().String()}
	processes.Log = localHostLog(home, run.Run.ID, current, masker)
	delegate := func(ctx context.Context, node string, generation, attempt int64) (nodehost.ProcessGrant, error) {
		principal, err := current(ctx)
		if err != nil {
			return nodehost.ProcessGrant{}, err
		}
		row, err := binding.Tenant.ReadNode(ctx, store.NodeRef{RunID: run.Run.ID, NodeID: node})
		if err != nil {
			return nodehost.ProcessGrant{}, err
		}
		if row.Generation != generation || row.Attempt != attempt {
			return nodehost.ProcessGrant{}, &apierr.Error{Code: apierr.LockHeld, Message: "process delegation differs from the current attempt"}
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return nodehost.ProcessGrant{}, err
		}
		fence := principal.Capability.Binding
		if fence.NodeID != node || fence.Generation != generation || fence.Attempt != attempt {
			return nodehost.ProcessGrant{}, &apierr.Error{Code: apierr.PolicyMissing, Message: "local owner needs its independently awarded node authority before process delegation"}
		}
		serveOnce.Do(func() { go func() { serveDone <- server.Serve(listener) }() })
		return processes.Delegate(ctx, lookup, capability.DelegateSpec{Parent: principal.Authority, Audience: capability.NodeHost,
			Grants: principal.Capability.Grants, Binding: fence, Repositories: principal.Capability.Repositories, Data: principal.Capability.Data, ExpiresAt: principal.Capability.ExpiresAt},
			nodehost.ProcessServices{RunID: run.Run.ID, Job: job})
	}
	follower := nodehost.StateFollower{Store: binding.Tenant, Subscribe: localStateSubscription}
	config := nodehost.NativeHostConfig{Store: binding.Tenant, Lease: lease.Keeper{Lease: limits.DefaultLease, Interval: limits.Renew(limits.DefaultLease)},
		Source: sourceOptions, Snapshots: snapshots, Masks: masks, Processes: processes, Router: router,
		Owner: nodehost.OwnerHost{Wait: follower.Wait, WaitNode: follower.WaitNode},
		Planner: nodehost.SDKPlanner{Log: func(ctx context.Context, frame nodeproto.FrameLog) error {
			return processes.Log(ctx, run.Run.ID, "", frame)
		}},
		Executor: nodehost.ExecutorDependencies{Bind: func(ctx context.Context, input nodehost.NodeInput) (nodehost.ProcessGrant, error) {
			return delegate(ctx, input.NodeID, input.Generation, input.Attempt)
		}},
		Evaluation: nodehost.EvaluationDependencies{
			Bind: func(ctx context.Context, input nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
				row, err := binding.Tenant.ReadNode(ctx, store.NodeRef{RunID: run.Run.ID, NodeID: input.NodeID})
				if err != nil {
					return nodehost.ProcessGrant{}, err
				}
				return delegate(ctx, input.NodeID, input.Generation, row.Attempt)
			},
			ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
				return nil, &apierr.Error{Code: apierr.PolicyMissing, Message: "local output reads require authorized byte and record bindings"}
			},
		},
	}
	return runners.NativeServices{Config: config, Close: closeResources}, nil
}

func localStateSubscription(ctx context.Context, _ string) (nodehost.StateSubscription, error) {
	ctx, cancel := context.WithCancel(ctx)
	changed, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}
	}()
	return nodehost.StateSubscription{Changed: changed, Close: func() error { cancel(); <-done; return nil }}, nil
}

func localHostLog(home, run string, current func(context.Context) (capability.Principal, error), masker *secrets.Masker) func(context.Context, string, string, nodeproto.FrameLog) error {
	var mu sync.Mutex
	return func(ctx context.Context, runID, node string, frame nodeproto.FrameLog) (err error) {
		if runID != run {
			return errors.New("log run differs from the host award")
		}
		if _, err := current(ctx); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		directory := filepath.Join(home, "run-logs")
		if err := fssecure.EnsureDir(directory); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(run))
		file, err := fssecure.OpenFile(filepath.Join(directory, hex.EncodeToString(digest[:])+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		encoded, err := json.Marshal(struct {
			Run, Job string
			Frame    nodeproto.FrameLog
		}{run, node, frame})
		if err != nil {
			return err
		}
		if masker == nil {
			return errors.New("local logs require the shared masker")
		}
		_, err = file.Write(append(masker.MaskJSON(encoded), '\n'))
		return err
	}
}
