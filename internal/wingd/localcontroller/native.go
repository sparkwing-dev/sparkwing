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
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NativeServices binds the elected local store to shared source, process and host services.
func NativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding, sourceOptions source.Options) (_ runners.NativeServices, err error) {
	if owned == nil || binding.Tenant == nil || binding.Refresh == nil || binding.Principal.Capability.Team != string(binding.Tenant.Team()) || binding.Principal.Capability.Binding.RunID == "" {
		return runners.NativeServices{}, errors.New("local native host requires its authenticated tenant and run")
	}
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
	claims := &nodeClaims{auth: capability.NewAuthenticator(owned), bindings: owned, masker: masker, current: current, readNode: binding.Tenant.ReadNode}
	masks := nodehost.NewMaskRegistry(masker)
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return runners.NativeServices{}, err
	}
	serve, closeHTTP := localProcessHTTP(ctx, listener, router, authorities.Close)
	closeResources = closeHTTP
	processes := &nodehost.NativeProcesses{Authorities: authorities, Masks: masks, URL: "http://" + listener.Addr().String()}
	claims.processes = processes
	processes.Log = localHostLog(home, run.Run.ID, current, masker)
	delegate := func(ctx context.Context, node string, generation, attempt int64) (nodehost.ProcessGrant, error) {
		grant, err := claims.delegate(ctx, node, generation, attempt)
		if err == nil {
			serve()
		}
		return grant, err
	}

	follower := nodehost.StateFollower{Store: binding.Tenant, Subscribe: localStateSubscription}
	config := nodehost.NativeHostConfig{
		Store: binding.Tenant, Lease: lease.Keeper{Lease: limits.DefaultLease, Interval: limits.Renew(limits.DefaultLease)},
		Source: sourceOptions, Snapshots: snapshots, Masks: masks, Processes: processes, Router: router,
		Owner: nodehost.OwnerHost{Wait: follower.Wait, WaitNode: follower.WaitNode, AdoptClaim: claims.adopt},
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

func localProcessHTTP(ctx context.Context, listener net.Listener, handler http.Handler, release func() error) (func(), func() error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	var startOnce, closeOnce sync.Once
	var closeErr error
	start := func() {
		startOnce.Do(func() {
			go func() { done <- wingd.ServeLocalHTTP(ctx, listener, handler, nil, nil) }()
		})
	}
	return start, func() error {
		closeOnce.Do(func() {
			cancel()
			start()
			closeErr = errors.Join(<-done, release())
		})
		return closeErr
	}
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
