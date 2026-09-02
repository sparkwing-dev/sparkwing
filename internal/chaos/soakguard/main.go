package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func main() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, terminationSignals()...)
	defer signal.Stop(signals)
	os.Exit(run(os.Args[1:], signals, nil))
}

func run(args []string, signals <-chan os.Signal, started chan<- int) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: soakguard command [args...]")
		return 2
	}
	// #nosec G702 -- the command named on this operator's command line, as argv without a shell
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "soakguard: start: %v\n", err)
		return 2
	}
	if started != nil {
		started <- group.ID()
	}

	forced := false
	select {
	case <-group.LeaderExited():
	case sig := <-signals:
		forced = true
		fmt.Fprintf(os.Stderr, "soakguard: received %s; terminating session %d\n", sig, group.ID())
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if forced {
			err = group.Terminate(ctx, 250*time.Millisecond)
		} else {
			err = group.Finish(ctx, 250*time.Millisecond)
		}
		cancel()
		if !errors.Is(err, procgroup.ErrCleanup) {
			break
		}
		forced = true
		fmt.Fprintf(os.Stderr, "soakguard: cleanup retry for session %d: %v\n", group.ID(), err)
		time.Sleep(100 * time.Millisecond)
	}
	if forced {
		return 130
	}
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "soakguard: wait: %v\n", err)
	return 1
}
