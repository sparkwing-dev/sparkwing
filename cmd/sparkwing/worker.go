package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/tokenpark"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runWorker(args []string) error {
	fs := flag.NewFlagSet(cmdWorker.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	poll := fs.Duration("poll", time.Second, "claim poll interval when the queue is empty")
	heartbeat := fs.Duration("heartbeat", 5*time.Second, "trigger claim heartbeat cadence")
	if err := parseAndCheck(cmdWorker, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		err := claimTriggers(ctx, *on, *poll, *heartbeat)
		if !errors.Is(err, errProfileChanged) {
			return err
		}
	}
}

var errProfileChanged = errors.New("the profile's config changed while its token was dead")

// safety: the change watch starts before the profile is read, so a rewrite
// racing the read costs one extra reload rather than an hour parked on the
// old token.
func claimTriggers(ctx context.Context, profileName string, poll, heartbeat time.Duration) error {
	var changed func() bool
	if path, err := profile.DefaultPath(); err == nil {
		changed = tokenpark.FileChanged(path)
	}
	prof, err := resolveProfile(profileName)
	if err != nil {
		return err
	}
	if err := requireController(prof, "worker"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sparkwing worker: profile=%s controller=%s poll=%s\n",
		prof.Name, prof.ControllerURL(), poll)

	token := prof.ControllerToken()
	cli := client.NewWithToken(prof.ControllerURL(), nil, token).
		WithRunnerIdentity(client.ProcessIdentity("worker"))
	shed := client.NewShedLog(client.ShedWarnInterval)
	pacer := client.NewPacer(poll)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		trigger, err := cli.ClaimTriggerFor(ctx, nil, nil)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			pace := pacer.Failure(err)
			switch {
			case pace.Dead != nil:
				fmt.Fprintf(os.Stderr, "worker: %s\n", pace.Dead.Explain(token,
					fmt.Sprintf("Give profile %q a live token; the worker reloads config.yaml within seconds of the rewrite", prof.Name)))
			case pace.Parked:
				fmt.Fprintf(os.Stderr, "worker: token %s is still refused; asking again in %s\n",
					client.TokenPrefix(token), pace.Wait.Round(time.Minute))
			case pace.Shed:
				if shed.Due() {
					fmt.Fprintf(os.Stderr,
						"worker: the controller is shedding claims; polling again in %s\n", pace.Wait)
				}
			default:
				fmt.Fprintf(os.Stderr, "worker: claim failed: %v (retrying in %s)\n", err, pace.Wait.Round(time.Millisecond))
			}
			watch := changed
			if !pace.Parked {
				watch = nil
			}
			if tokenpark.Wait(ctx, pace.Wait, watch) {
				fmt.Fprintln(os.Stderr, "worker: config.yaml changed while parked; reloading the profile")
				return errProfileChanged
			}
			continue
		}
		if pacer.Success() {
			fmt.Fprintln(os.Stderr, "worker: token accepted again; resuming claims")
		}
		if trigger == nil {
			sleepOrCancel(ctx, client.AdvisedPoll(poll, cli))
			continue
		}
		fmt.Fprintf(os.Stderr, "worker: claimed %s (pipeline=%s)\n", trigger.ID, trigger.Pipeline)
		if err := hostTrigger(ctx, cli, trigger, prof.ControllerURL(), token, heartbeat); err != nil {
			fmt.Fprintf(os.Stderr, "worker: trigger %s: %v\n", trigger.ID, err)
		}
	}
}

// safety: the run is hosted in this process from the pipeline this checkout
// builds; plan code and every node run in its own process with the minimal
// environment, so the profile's token stays here.
// safety: the claim is renewed from before the build, so a cold build longer
// than the claim's lease keeps it; a worker shutting down leaves the trigger
// to that lease, and a run that never started fails here, or ends cancelled
// for an operator's cancel.
func hostTrigger(ctx context.Context, cli *client.Client, trigger *store.Trigger, controllerURL, token string, heartbeat time.Duration) error {
	return hostTriggerWith(ctx, cli, trigger, controllerURL, token, heartbeat, buildCheckoutPipeline, orchestrator.HostTriggerRun)
}

type checkoutBuild func(ctx context.Context) (binary, dir string, release func(), err error)

func hostTriggerWith(ctx context.Context, cli *client.Client, trigger *store.Trigger, controllerURL, token string,
	heartbeat time.Duration, build checkoutBuild, host func(context.Context, orchestrator.TriggerHost) error,
) error {
	runCtx, stop := orchestrator.HeartbeatTrigger(orchestrator.DispatchContext(ctx, trigger), cli, trigger.ID, heartbeat, slog.Default())
	defer stop()
	err := hostTriggerFromCheckout(runCtx, trigger, controllerURL, token, build, host)
	cause := context.Cause(runCtx)
	if err == nil || errors.Is(cause, context.Canceled) {
		return err
	}
	status := "failed"
	if cancel := (*nodehost.CancelError)(nil); errors.As(cause, &cancel) {
		status = "cancelled"
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
	defer cancel()
	if ferr := cli.FinishRun(finishCtx, trigger.ID, status, err.Error()); ferr != nil &&
		!orchestrator.ConfirmTerminalRunAfterWriteError(finishCtx, cli, trigger.ID) {
		return errors.Join(err, fmt.Errorf("finish the run: %w", ferr))
	}
	if ferr := cli.FinishTrigger(finishCtx, trigger.ID); ferr != nil {
		return errors.Join(err, fmt.Errorf("finish the trigger: %w", ferr))
	}
	return err
}

func buildCheckoutPipeline(ctx context.Context) (binary, dir string, release func(), err error) {
	sparkwingDir, err := findSparkwingDir()
	if err != nil {
		return "", "", nil, err
	}
	key, keyParts, err := bincache.ExplainCacheKey(sparkwingDir)
	if err != nil {
		return "", "", nil, fmt.Errorf("read .sparkwing/: %w", err)
	}
	lease, _, err := pipelineBinary(ctx, sparkwingDir, key, keyParts, os.Environ())
	if err != nil {
		return "", "", nil, fmt.Errorf("build the pipeline: %w", err)
	}
	return lease.Path(), filepath.Dir(sparkwingDir), func() {
		if err := lease.Release(); err != nil {
			slog.Default().Debug("pipeline binary lease release failed", "err", err)
		}
	}, nil
}

func hostTriggerFromCheckout(ctx context.Context, trigger *store.Trigger, controllerURL, token string,
	build checkoutBuild, host func(context.Context, orchestrator.TriggerHost) error,
) error {
	binary, dir, release, err := build(ctx)
	if err != nil {
		return err
	}
	defer release()
	return host(ctx, orchestrator.TriggerHost{
		ControllerURL: controllerURL, Token: token, Trigger: trigger, Binary: binary, Dir: dir,
	})
}

func sleepOrCancel(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
