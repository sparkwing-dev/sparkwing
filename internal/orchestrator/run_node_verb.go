package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// RunNodeCommand executes a single claimed node from the command line and
// returns the node's failure, if any. Both the sparkwing CLI and
// sparkwing-runner expose it as their `run-node` verb, so a Kubernetes Job can
// invoke either binary:
//
//	RunNodeCommand([]string{"--controller", "https://controller", runID, nodeID})
//
// The run and node ids may come from the positional arguments or from
// SPARKWING_RUN_ID and SPARKWING_NODE_ID.
//
// When SPARKWING_NODE_CLAIM_HOLDER and SPARKWING_NODE_CLAIM_GENERATION name a
// claim the dispatcher already took for this node, every state write and log
// append carries that claim's fence and the claim is renewed until the node
// exits. Without them the node runs unfenced, which is what an unclaimed local
// invocation wants.
func RunNodeCommand(args []string) error {
	fs := flag.NewFlagSet("run-node", flag.ExitOnError)
	controllerURL := fs.String("controller", ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"), "controller base URL")
	logsURL := fs.String("logs", ResolveDevEnvURL("SPARKWING_LOGS_URL"), "logs-service URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	runID := fs.Arg(0)
	if runID == "" {
		runID = os.Getenv("SPARKWING_RUN_ID")
	}
	nodeID := fs.Arg(1)
	if nodeID == "" {
		nodeID = os.Getenv("SPARKWING_NODE_ID")
	}
	apiSocket := os.Getenv(wingwire.APISocketEnv)
	if (*controllerURL == "" && apiSocket == "") || runID == "" || nodeID == "" {
		fs.Usage()
		return errors.New("--controller (or " + wingwire.APISocketEnv + ") + <runID> + <nodeID> are required")
	}

	// safety: leave SIGTERM unhandled; bounce, cancellation, and pod termination
	// rely on the supervisor rather than the killed node to record the outcome.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, abandon := context.WithCancelCause(ctx)
	defer abandon(nil)

	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	var runOpts []RunNodeOption
	if apiSocket != "" {
		runOpts = append(runOpts, OverAPISocket(apiSocket))
		token = ""
	}

	holderID := fmt.Sprintf("pod:%s:%s", runID, nodeID)
	fence, lease, err := dispatchedNodeClaim()
	if err != nil {
		return err
	}
	if fence.HolderID != "" {
		holderID = fence.HolderID
		runOpts = append(runOpts, ClaimedNodeFence(fence))
		// safety: the renewal has to reach the controller the node's writes
		// reach, which over the daemon's unix socket is neither the URL nor the
		// bearer token this process was given.
		transports := nodeTransportsFor(runNodeConfig{apiSocket: apiSocket}, *controllerURL, token)
		defer transports.close()
		var wg sync.WaitGroup
		hbCtx, stopHeartbeat := context.WithCancel(ctx)
		// safety: nothing releases the claim, so the pod stops renewing it and
		// the lease lapses for the reaper on every exit path, panic included.
		defer func() {
			stopHeartbeat()
			wg.Wait()
		}()
		if transports.stateURL != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				heartbeatDispatchedClaim(hbCtx,
					client.NewWithToken(transports.stateURL, transports.state, transports.stateToken),
					runID, nodeID, fence, lease, abandon, slog.Default())
			}()
		}
	}

	// safety: the pod runs the team's code, so its dispatcher hands it no cache
	// credential; it asks for its own run's grant, which the source fetch, the
	// binary cache, the artifact store and the node's steps all read from here.
	if apiSocket == "" && os.Getenv(authwire.CacheGrantEnv) == "" {
		grantCtx := ctx
		if fence.HolderID != "" {
			grantCtx = store.WithNodeClaimFence(ctx, fence)
		}
		if grant := RequestRunCacheGrant(grantCtx, *controllerURL, token, runID, slog.Default()); grant != "" {
			if err := os.Setenv(authwire.CacheGrantEnv, grant); err != nil {
				return fmt.Errorf("hand the node its cache grant: %w", err)
			}
		}
	}

	res, err := RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID,
		holderID, token, NewJSONRenderer(), slog.Default(), nil, runOpts...)
	// safety: a step that swallows its cancellation must still leave the pod
	// failing, so the Job ends rather than being retried as a success.
	if cause := context.Cause(ctx); errors.Is(cause, errClaimAbandoned) {
		return cause
	}
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// safety: runNodeCLI reads the same variables and supervises an isolated child
// under them; here the process already is that child, so the fence guards its
// own writes. A lease the dispatcher did not name falls back to the store's cap
// so one missed renewal does not lose the claim.
func dispatchedNodeClaim() (store.NodeClaimFence, time.Duration, error) {
	holder := os.Getenv("SPARKWING_NODE_CLAIM_HOLDER")
	if holder == "" {
		return store.NodeClaimFence{}, 0, nil
	}
	generation, err := strconv.ParseInt(os.Getenv("SPARKWING_NODE_CLAIM_GENERATION"), 10, 64)
	if err != nil || generation < 1 {
		return store.NodeClaimFence{}, 0, errors.New(
			"SPARKWING_NODE_CLAIM_GENERATION must name the awarded claim generation")
	}
	lease := store.MaxLeaseDuration
	if raw := os.Getenv("SPARKWING_NODE_CLAIM_LEASE_SECONDS"); raw != "" {
		secs, convErr := strconv.Atoi(raw)
		if convErr != nil || secs < 1 {
			return store.NodeClaimFence{}, 0, errors.New(
				"SPARKWING_NODE_CLAIM_LEASE_SECONDS must be a positive number of seconds")
		}
		lease = time.Duration(secs) * time.Second
	}
	return store.NodeClaimFence{
		HolderID:        holder,
		MembershipID:    os.Getenv("SPARKWING_NODE_CLAIM_MEMBERSHIP"),
		ReservationID:   os.Getenv("SPARKWING_NODE_CLAIM_RESERVATION"),
		ClaimGeneration: generation,
	}, lease, nil
}

// errClaimAbandoned ends a dispatched node whose claim the controller will
// no longer renew.
var errClaimAbandoned = errors.New("run-node: the controller no longer renews this node's claim, so the node stopped")

// safety: the controller refuses a renewal when the team's credits run out,
// the claim was reaped, or the node was cancelled, and a pod that kept going
// would bill compute until the Job deadline. A controller unreachable for a
// whole lease has let the claim lapse, so the pod stops then too.
func heartbeatDispatchedClaim(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID string,
	fence store.NodeClaimFence,
	lease time.Duration,
	abandon context.CancelCauseFunc,
	logger *slog.Logger,
) {
	ctx = store.WithNodeClaimFence(ctx, fence)
	lastOK := time.Now()
	renew := func() (stop bool) {
		err := ctrl.HeartbeatNodeClaim(ctx, runID, nodeID, fence.HolderID, lease, nil)
		switch {
		case err == nil:
			lastOK = time.Now()
		case ctx.Err() != nil:
			return true
		case errors.Is(err, store.ErrLockHeld):
			logger.Error("run-node: the controller refused the claim renewal; stopping the node",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID)
			abandon(fmt.Errorf("%w: %w", errClaimAbandoned, err))
			return true
		case time.Since(lastOK) >= lease:
			logger.Error("run-node: the controller was unreachable for the whole claim lease; stopping the node",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID, "err", err)
			abandon(fmt.Errorf("%w: %w", errClaimAbandoned, err))
			return true
		default:
			logger.Warn("run-node: claim heartbeat failed",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID, "err", err)
		}
		return false
	}
	// safety: the pod's first renewal is where the controller starts billing a
	// dispatched node, so it goes out as the pod starts rather than one
	// interval later; the fetch and compile after it are the customer's work.
	if renew() {
		return
	}
	t := time.NewTicker(store.DispatchedHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if renew() {
				return
			}
		}
	}
}
