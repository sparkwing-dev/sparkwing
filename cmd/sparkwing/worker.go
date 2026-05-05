// `sparkwing cluster worker` -- laptop-local worker that claims
// triggers from a profile's controller and dispatches each one to
// the user's compiled `.sparkwing/` binary via `handle-trigger <id>`.
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

	"github.com/sparkwing-dev/sparkwing/controller/client"
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
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "worker"); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}

	fmt.Fprintf(os.Stderr, "sparkwing worker: profile=%s controller=%s logs=%s poll=%s\n",
		prof.Name, prof.Controller, prof.Logs, *poll)

	cli := client.NewWithToken(prof.Controller, nil, prof.Token)
	// Empty pipeline and source filters = accept any trigger. The
	// claim loop here doesn't know the consumer's registry; the
	// spawned handle-trigger child will reject the trigger at Plan()
	// time if it doesn't recognize the pipeline.
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		trigger, err := cli.ClaimTriggerFor(ctx, nil, nil)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			fmt.Fprintf(os.Stderr, "worker: claim failed: %v (retrying)\n", err)
			sleepOrCancel(ctx, *poll)
			continue
		}
		if trigger == nil {
			sleepOrCancel(ctx, *poll)
			continue
		}
		fmt.Fprintf(os.Stderr, "worker: claimed %s (pipeline=%s)\n", trigger.ID, trigger.Pipeline)
		dispatchTrigger(ctx, self, trigger.ID, prof.Controller, prof.Logs, prof.Token, *heartbeat)
	}
}

// dispatchTrigger hands a claimed trigger to `sparkwing handle-trigger`
// as a child process. That command routes through runWing's
// compile+exec, so the user's .sparkwing binary runs the pipeline
// with its registry intact. The child's exit status is observed but
// not propagated -- the trigger is already flipped to 'done' inside
// the child's HandleClaimedTrigger (even on setup failure), so we
// just log and go back to claiming.
func dispatchTrigger(ctx context.Context, self, triggerID, controllerURL, logsURL, token string, heartbeat time.Duration) {
	args := []string{
		"handle-trigger",
		triggerID,
		"--controller", controllerURL,
		"--heartbeat", heartbeat.String(),
	}
	if logsURL != "" {
		args = append(args, "--logs", logsURL)
	}
	if token != "" {
		args = append(args, "--token", token)
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Env = os.Environ()
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
