package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const hereRunnerFlag = "here"

const hereHeartbeat = 5 * time.Second

func checkHereFlags(flags triggerFlags) error {
	switch {
	case flags.runner != hereRunnerFlag && flags.reason != "":
		return errors.New("--reason is recorded only with --runner here")
	case flags.runner != hereRunnerFlag:
		return nil

	case strings.TrimSpace(flags.reason) == "":
		return errors.New("--runner here needs --reason TEXT, which the run records")
	case flags.detach:
		return errors.New("--runner here hosts the run in this process, so it cannot --detach")
	case flags.workingTree:
		return errors.New("--runner here builds this checkout as it is; commit the change instead of passing --working-tree")
	}
	return nil
}

// safety: the run records the commit HEAD names, so a tracked change that
// would run without being recorded is refused.
func refuseUncommittedCheckout() error {
	out, err := gitOutput(context.Background(), ".", nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("pipeline trigger: read the checkout's status: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("pipeline trigger: --runner here builds this checkout and the run records HEAD, " +
			"so commit the tracked changes first")
	}
	return nil
}

// safety: a pinned run no other runner may take would wait forever once this
// process stops hosting it, so an interrupt first asks the controller to cancel
// it and only a second one leaves it.
func hostAndFollowHere(prof *profile.Profile, pipeline string, resp *client.TriggerResponse) error {
	if resp.RunnerToken == "" {
		return fmt.Errorf("pipeline trigger: the controller queued %s without a runner credential", resp.RunID)
	}
	fmt.Fprintf(os.Stderr, "triggered %s on %s as %s; hosting it on this machine (runner credential expires %s)\n",
		pipeline, prof.Name, resp.RunID, time.Unix(resp.RunnerTokenExpiresAt, 0).Format(time.Kitchen))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	followCtx, stopFollow := context.WithCancel(ctx)
	defer stopFollow()
	go cancelRunOnInterrupt(ctx, stop, prof, resp.RunID)

	hosted := make(chan error, 1)
	go func() {
		err := hostHereRun(ctx, prof.ControllerURL(), resp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pipeline trigger: hosting %s on this machine: %v\n", resp.RunID, err)
			// safety: a pinned run no other runner may take would wait forever, so a host that failed cancels it.
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cerr := profileClient(prof).CancelRun(cancelCtx, resp.RunID); cerr != nil {
				fmt.Fprintf(os.Stderr, "pipeline trigger: cancel %s: %v\n", resp.RunID, cerr)
			}
			stopFollow()
		}
		hosted <- err
	}()

	format, err := resolveTTYAwareOutput("", "pipeline trigger")
	if err != nil {
		stop()
		<-hosted
		return err
	}
	followErr := followControllerRunLogs(followCtx, client.EngineOptions{
		URL:    prof.ControllerURL(),
		Bearer: func(context.Context) (string, error) { return prof.ControllerToken(), nil },
	}, resp.RunID, format, os.Stdout)
	<-hosted
	return remoteFollowExit(context.Background(), prof, resp.RunID, followErr)
}

func hostHereRun(ctx context.Context, controllerURL string, resp *client.TriggerResponse) error {
	cli := client.NewWithToken(controllerURL, nil, resp.RunnerToken).WithRunnerIdentity(client.ProcessIdentity("here"))
	trigger, err := cli.ClaimSpecificTrigger(ctx, resp.RunID, 0)
	if err != nil {
		return fmt.Errorf("claim the run: %w", err)
	}
	return hostTrigger(ctx, cli, trigger, controllerURL, resp.RunnerToken, hereHeartbeat)
}

func profileClient(prof *profile.Profile) *client.Client {
	return client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
}

func cancelRunOnInterrupt(ctx context.Context, stop context.CancelFunc, prof *profile.Profile, runID string) {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	select {
	case <-sigs:
	case <-ctx.Done():
		return
	}
	fmt.Fprintf(os.Stderr, "cancelling %s; interrupt again to stop hosting it without waiting\n", runID)
	if err := profileClient(prof).CancelRun(ctx, runID); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline trigger: cancel %s: %v\n", runID, err)
	}
	select {
	case <-sigs:
		stop()
	case <-ctx.Done():
	}
}
