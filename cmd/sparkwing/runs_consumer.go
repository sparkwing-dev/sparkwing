// `sparkwing runs consumer {start,status,stop}` and the detached body
// behind them -- lifecycle for the process that executes submitted runs.
//
// The installed distribution owns this process, the same rule the
// admission daemon follows: this CLI re-execs itself as the consumer, so
// the binary serving a home's queue is always one that knows the verb.
// A compiled pipeline binary never hosts one.
//
// Most people never type these verbs. `runs submit` starts a consumer
// when none is running, and the consumer exits on its own after a quiet
// window. They exist for the operator who wants to see whether one is
// resident, and for stopping it deliberately.
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

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// consumerSpawnVerb is the hidden subcommand a spawned consumer is
// started with. Like the dashboard supervisor's, it is not in the
// command registry: it is an implementation detail of the spawn, not a
// surface anyone should type.
const consumerSpawnVerb = "__runs-consume"

// consumerStartTimeout bounds how long a spawn waits to see the new
// consumer take the election lock. Generous because the consumer opens
// (and may migrate) the state database on the way up, and a loaded
// machine should not read as a failed start.
const consumerStartTimeout = 20 * time.Second

// consumerStartPoll is how often the wait re-checks the lock.
const consumerStartPoll = 25 * time.Millisecond

func runRunsConsumer(args []string) error {
	if handleParentHelp(cmdJobsConsumer, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdJobsConsumer, os.Stdout)
		return nil
	}
	switch args[0] {
	case "start":
		return runRunsConsumerStart(args[1:])
	case "status":
		return runRunsConsumerStatus(args[1:])
	case "stop", "kill":
		return runRunsConsumerStop(args[1:])
	default:
		PrintHelp(cmdJobsConsumer, os.Stderr)
		return fmt.Errorf("runs consumer: unknown subcommand %q", args[0])
	}
}

func runRunsConsumerStart(args []string) error {
	fs := flag.NewFlagSet(cmdJobsConsumerStart.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	idle := fs.Duration("idle", 0, "exit after this long with no work (default 5m; 0 means the default)")
	claimLease := fs.Duration("claim-lease", 0,
		"lease stamped on each claimed run, renewed while it executes (default 3m)")
	if err := parseAndCheck(cmdJobsConsumerStart, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	layout, err := orchestrator.ConsumerLayoutFor(*home)
	if err != nil {
		return err
	}
	if err := ensureTriggerConsumer(layout.Home, *idle, *claimLease); err != nil {
		return err
	}
	pid, _ := orchestrator.ConsumerPID(layout.Home)
	fmt.Fprintf(os.Stdout, "trigger consumer running (pid %d)\n", pid)
	fmt.Fprintf(os.Stdout, "  home: %s\n", layout.Home)
	fmt.Fprintf(os.Stdout, "  log:  %s\n", layout.Log)
	return nil
}

func runRunsConsumerStatus(args []string) error {
	fs := flag.NewFlagSet(cmdJobsConsumerStatus.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdJobsConsumerStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	layout, err := orchestrator.ConsumerLayoutFor(*home)
	if err != nil {
		return err
	}
	running, err := orchestrator.ConsumerRunning(layout.Home)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintln(os.Stdout, "trigger consumer not running")
		return exitErrorf(1, "not running")
	}
	pid, _ := orchestrator.ConsumerPID(layout.Home)
	fmt.Fprintf(os.Stdout, "trigger consumer running (pid %d)\n", pid)
	fmt.Fprintf(os.Stdout, "  home: %s\n", layout.Home)
	fmt.Fprintf(os.Stdout, "  log:  %s\n", layout.Log)
	return nil
}

func runRunsConsumerStop(args []string) error {
	fs := flag.NewFlagSet(cmdJobsConsumerStop.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdJobsConsumerStop, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	layout, err := orchestrator.ConsumerLayoutFor(*home)
	if err != nil {
		return err
	}
	pid, ok := orchestrator.ConsumerPID(layout.Home)
	if !ok {
		fmt.Fprintln(os.Stdout, "trigger consumer not running")
		return nil
	}
	if err := stopSupervisor(pid, layout.PID); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "trigger consumer stopped (pid %d)\n", pid)
	fmt.Fprintln(os.Stdout, "queued runs stay queued; the next `sparkwing runs submit` starts a consumer again")
	return nil
}

// ensureTriggerConsumer guarantees a consumer owns home's queue before
// the caller acknowledges anything, spawning one when none is resident.
//
// The wait is the load-bearing part. Returning as soon as the child
// process starts would acknowledge a run whose executor might still die
// during startup -- a schema-skew refusal, an unwritable home. Waiting
// for the election lock to be held means the acknowledgment is backed by
// a process that got far enough to own the queue.
//
// A consumer that is already resident satisfies this immediately; the
// spawn is only for the cold case.
func ensureTriggerConsumer(home string, idle, claimLease time.Duration) error {
	running, err := orchestrator.ConsumerRunning(home)
	if err != nil {
		return err
	}
	if running {
		if !rotateOutdatedConsumer(home) {
			return nil
		}
	}
	if err := spawnTriggerConsumer(home, idle, claimLease); err != nil {
		return err
	}
	deadline := time.Now().Add(consumerStartTimeout)
	for time.Now().Before(deadline) {
		if running, err := orchestrator.ConsumerRunning(home); err == nil && running {
			return nil
		}
		time.Sleep(consumerStartPoll)
	}
	layout, lerr := orchestrator.ConsumerLayoutFor(home)
	if lerr != nil {
		return fmt.Errorf("consumer did not take the queue lock within %s", consumerStartTimeout)
	}
	tail := tailFile(layout.Log, 20)
	if tail == "" {
		tail = "(the consumer wrote nothing to its log)"
	}
	return fmt.Errorf("consumer did not take the queue lock within %s; %s:\n%s",
		consumerStartTimeout, layout.Log, tail)
}

// rotateOutdatedConsumer stops a resident consumer built from a
// different sparkwing version than this CLI, and reports whether the
// queue is now free for a fresh one.
//
// Without it an upgrade silently does not take. A consumer keeps its
// queue for as long as work keeps arriving, and `runs submit` only ever
// asked whether *a* consumer was running -- so on a busy home the newly
// installed binary would hand every run to the old build indefinitely,
// including runs submitted precisely to pick up a fix.
//
// A consumer that records no version predates the stamp; it is rotated
// too, since an unknown build is exactly the stale case. Failing to stop
// it is not fatal: the existing consumer still executes the run, which
// is better than refusing to submit.
func rotateOutdatedConsumer(home string) bool {
	info, ok := orchestrator.ConsumerInfo(home)
	if !ok {
		return true
	}
	mine := installedVersion()
	if info.Version == mine {
		return false
	}
	fmt.Fprintf(os.Stderr,
		"sparkwing runs submit: replacing the resident consumer (pid %d, %s) with this build (%s)\n",
		info.PID, consumerVersionLabel(info.Version), mine)
	if err := stopSupervisor(info.PID, ""); err != nil {
		fmt.Fprintf(os.Stderr,
			"sparkwing runs submit: could not stop the older consumer (%v); it keeps serving this home\n", err)
		return false
	}
	deadline := time.Now().Add(consumerStartTimeout)
	for time.Now().Before(deadline) {
		if running, err := orchestrator.ConsumerRunning(home); err == nil && !running {
			return true
		}
		time.Sleep(consumerStartPoll)
	}
	return false
}

func consumerVersionLabel(v string) string {
	if v == "" {
		return "version unrecorded"
	}
	return v
}

// spawnTriggerConsumer re-execs this binary as a detached consumer.
//
// Detaching from the terminal's process group is what makes a submitted
// run survive the submitting shell: without Setsid, a Ctrl-C in that
// shell reaches the consumer through the foreground process group and
// kills the very thing that was supposed to outlive it.
func spawnTriggerConsumer(home string, idle, claimLease time.Duration) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	layout, err := orchestrator.ConsumerLayoutFor(home)
	if err != nil {
		return err
	}
	if err := fssecure.EnsureDir(layout.Home); err != nil {
		return fmt.Errorf("mkdir %s: %w", layout.Home, err)
	}
	logF, err := fssecure.OpenFile(layout.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("open %s: %w", layout.Log, err)
	}
	defer func() { _ = logF.Close() }()

	spawnArgs := []string{consumerSpawnVerb, "--home", layout.Home,
		"--version", installedVersion()}
	if idle > 0 {
		spawnArgs = append(spawnArgs, "--idle", idle.String())
	}
	if claimLease > 0 {
		spawnArgs = append(spawnArgs, "--claim-lease", claimLease.String())
	}
	cmd := exec.Command(self, spawnArgs...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	// The consumer serves the home it was given, and so must everything
	// it launches. Each dispatched run re-execs a pipeline binary that
	// resolves its own paths from $SPARKWING_HOME; inheriting a different
	// value than --home would have the consumer claim triggers from one
	// store and the run it dispatched write its results to another.
	cmd.Env = setEnv(os.Environ(), "SPARKWING_HOME", layout.Home)
	cmd.SysProcAttr = newDetachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn trigger consumer: %w", err)
	}
	// safety: reap the detached child so an immediate exit does not leave
	// a zombie. A clean early exit is the normal election-loss outcome
	// when two submissions race to spawn, so it is not reported.
	go func() { _ = cmd.Wait() }()
	return nil
}

// runRunsConsumeDetached is the body of the spawned consumer. It serves
// until the idle window elapses or it is signalled.
//
// Losing the election exits zero on purpose: two submissions racing to
// spawn both start a process, one wins the lock, and the loser has
// nothing to complain about -- the queue is owned, which is all either
// caller wanted.
func runRunsConsumeDetached(args []string) error {
	fs := flag.NewFlagSet(consumerSpawnVerb, flag.ContinueOnError)
	home := fs.String("home", "", "")
	idle := fs.Duration("idle", 0, "")
	lease := fs.Duration("claim-lease", 0, "")
	version := fs.String("version", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := orchestrator.ServeConsumer(ctx, orchestrator.ConsumerOptions{
		Home:        *home,
		IdleTimeout: *idle,
		ClaimLease:  *lease,
		Version:     *version,
	})
	if errors.Is(err, orchestrator.ErrConsumerElectionLost) {
		return nil
	}
	return err
}
