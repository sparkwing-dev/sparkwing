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

const consumerSpawnVerb = "__runs-consume"

const consumerStartTimeout = 20 * time.Second

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
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain")
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
	mode, err := resolveOutputFormat(*output, fs.Name())
	if err != nil {
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
	return writeServiceStatus(os.Stdout, serviceStatus{Service: "consumer", State: "running", PID: pid, Home: layout.Home, Log: layout.Log}, mode, fmt.Sprintf("trigger consumer running (pid %d)\n  home: %s\n  log:  %s\n", pid, layout.Home, layout.Log))
}

func runRunsConsumerStatus(args []string) error {
	fs := flag.NewFlagSet(cmdJobsConsumerStatus.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdJobsConsumerStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	mode, err := resolveOutputFormat(*output, fs.Name())
	if err != nil {
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
		if err := writeServiceStatus(os.Stdout, serviceStatus{Service: "consumer", State: "stopped", Home: layout.Home, Log: layout.Log}, mode, "trigger consumer not running\n"); err != nil {
			return err
		}
		return exitErrorf(1, "not running")
	}
	pid, _ := orchestrator.ConsumerPID(layout.Home)
	return writeServiceStatus(os.Stdout, serviceStatus{Service: "consumer", State: "running", PID: pid, Home: layout.Home, Log: layout.Log}, mode, fmt.Sprintf("trigger consumer running (pid %d)\n  home: %s\n  log:  %s\n", pid, layout.Home, layout.Log))
}

func runRunsConsumerStop(args []string) error {
	fs := flag.NewFlagSet(cmdJobsConsumerStop.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdJobsConsumerStop, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	mode, err := resolveOutputFormat(*output, fs.Name())
	if err != nil {
		return err
	}

	layout, err := orchestrator.ConsumerLayoutFor(*home)
	if err != nil {
		return err
	}
	pid, ok := orchestrator.ConsumerPID(layout.Home)
	if !ok {
		return writeServiceStatus(os.Stdout, serviceStatus{Service: "consumer", State: "stopped", Home: layout.Home, Log: layout.Log}, mode, "trigger consumer not running\n")
	}
	if err := stopSupervisor(pid, layout.PID); err != nil {
		return err
	}
	return writeServiceStatus(os.Stdout, serviceStatus{Service: "consumer", State: "stopped", PID: pid, Home: layout.Home, Log: layout.Log}, mode, fmt.Sprintf("trigger consumer stopped (pid %d)\nqueued runs stay queued; the next `sparkwing run <pipeline> --sw-detached` starts a consumer again\n", pid))
}

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

func rotateOutdatedConsumer(home string) bool {
	info, ok := orchestrator.ConsumerInfo(home)
	if !ok {
		return true
	}
	mine := installedVersion()
	if info.Version == mine {
		return false
	}
	if info.Host != "" {
		fmt.Fprintf(os.Stderr,
			"sparkwing run --sw-detached: the %s (pid %d, %s) hosts this home's consumer; the run executes under that build\n",
			info.Host, info.PID, consumerVersionLabel(info.Version))
		return false
	}
	fmt.Fprintf(os.Stderr,
		"sparkwing run --sw-detached: replacing the resident consumer (pid %d, %s) with this build (%s)\n",
		info.PID, consumerVersionLabel(info.Version), mine)
	if err := stopSupervisor(info.PID, ""); err != nil {
		fmt.Fprintf(os.Stderr,
			"sparkwing run --sw-detached: could not stop the older consumer (%v); it keeps serving this home\n", err)
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

	spawnArgs := []string{
		consumerSpawnVerb, "--home", layout.Home,
		"--version", installedVersion(),
	}
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
