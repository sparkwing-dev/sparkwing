package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/tokenpark"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func runWorker(args []string) error {
	fs := flag.NewFlagSet(cmdWorker.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	poll := fs.Duration("poll", time.Second, "claim poll interval when the queue is empty")
	heartbeat := fs.Duration("heartbeat", 5*time.Second, "heartbeat cadence passed to handle-trigger")
	if err := parseAndCheck(cmdWorker, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		err := claimTriggers(ctx, self, *on, *poll, *heartbeat)
		if !errors.Is(err, errProfileChanged) {
			return err
		}
	}
}

var errProfileChanged = errors.New("the profile's config changed while its token was dead")

// safety: the change watch starts before the profile is read, so a rewrite
// racing the read costs one extra reload rather than an hour parked on the
// old token.
func claimTriggers(ctx context.Context, self, profileName string, poll, heartbeat time.Duration) error {
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
	logsURL := prof.ExplicitLogsURL()
	if logsURL == "" {
		if logsURL, err = orchestrator.DiscoverLogsURL(ctx, prof.ControllerURL(), token); err != nil {
			return fmt.Errorf("worker: %w", err)
		}
	}
	cli := client.NewWithToken(prof.ControllerURL(), nil, token).
		WithRunnerIdentity(logs.ProcessIdentity("worker"))
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
		dispatchTrigger(ctx, self, trigger.ID, prof.ControllerURL(), logsURL, token, heartbeat)
	}
}

func dispatchTrigger(ctx context.Context, self, triggerID, controllerURL, logsURL, token string, heartbeat time.Duration) {
	args := append([]string{"handle-trigger"}, orchestrator.HandleTriggerArgs(triggerID, controllerURL, logsURL, heartbeat)...)
	cmd := exec.CommandContext(ctx, self, args...)
	// safety: the child reads the bearer from SPARKWING_AGENT_TOKEN; argv is world-readable in /proc.
	cmd.Env = os.Environ()
	if token != "" {
		cmd.Env = append(cmd.Env, "SPARKWING_AGENT_TOKEN="+token)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "worker: handle-trigger %s exited: %v\n", triggerID, err)
	}
}

func sleepOrCancel(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
