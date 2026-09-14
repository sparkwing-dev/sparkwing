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
					runID, nodeID, fence, lease, slog.Default())
			}()
		}
	}

	res, err := RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID,
		holderID, token, NewJSONRenderer(), slog.Default(), nil, runOpts...)
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// DispatchedClaimHeartbeatInterval is how often a dispatched node renews the
// claim its dispatcher took for it. The dispatcher renews nothing, so this is
// the only signal that the pod is alive.
const DispatchedClaimHeartbeatInterval = store.DispatchedHeartbeatInterval

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

func heartbeatDispatchedClaim(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID string,
	fence store.NodeClaimFence,
	lease time.Duration,
	logger *slog.Logger,
) {
	ctx = store.WithNodeClaimFence(ctx, fence)
	t := time.NewTicker(DispatchedClaimHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := ctrl.HeartbeatNodeClaim(ctx, runID, nodeID, fence.HolderID, lease, nil); err != nil {
				logger.Debug("run-node: claim heartbeat failed",
					"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID, "err", err)
			}
		}
	}
}
