package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const queueExecCleanupTimeout = 10 * time.Second

func runQueueExec(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runQueueExecContext(ctx, args)
}

func runQueueExecContext(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdQueueExec.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing state directory")
	runID := fs.String("run-id", "", "unique admission participant identifier")
	name := fs.String("name", "", "short operation name shown in the queue")
	repo := fs.String("repo", "", "repository name shown in the queue")
	cores := fs.Float64("cores", 0, "CPU cores reserved while the command runs")
	memoryBytes := fs.Int64("memory-bytes", 0, "memory bytes reserved while the command runs")
	semaphore := fs.String("semaphore", "", "logical semaphore shared with equivalent commands")
	semaphoreCapacity := fs.Int("semaphore-capacity", 1, "capacity declared for --semaphore")
	readyFile := fs.String("ready-file", "", "write admission readiness to a new JSON file")
	if err := parseAndCheck(cmdQueueExec, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	command := fs.Args()
	if len(command) == 0 {
		return fmt.Errorf("queue exec: command is required after --")
	}
	if *cores <= 0 {
		return fmt.Errorf("queue exec: --cores must be greater than zero")
	}
	if *memoryBytes < 0 {
		return fmt.Errorf("queue exec: --memory-bytes must not be negative")
	}
	if *semaphore != "" && *semaphoreCapacity <= 0 {
		return fmt.Errorf("queue exec: --semaphore-capacity must be greater than zero")
	}

	acquireCtx, cancelAcquire := context.WithCancel(ctx)
	defer cancelAcquire()
	cl, err := wingdclient.EnsureDaemon(acquireCtx, wingdclient.Options{Home: *home, Version: Version})
	if err != nil {
		return fmt.Errorf("queue exec: connect admission daemon: %w", err)
	}
	request := wingwire.AdmissionRequest{
		RunID:        *runID,
		DisplayRunID: *runID,
		Pipeline:     *name,
		Repo:         *repo,
		PID:          os.Getpid(),
		Resources:    wingwire.HostResources{Cores: *cores, MemoryBytes: *memoryBytes},
	}
	if *semaphore != "" {
		request.Semaphores = []wingwire.SemaphoreClaim{{
			Name: *semaphore, Cost: 1, Capacity: *semaphoreCapacity, Policy: wingwire.PolicyQueue,
		}}
	}
	var readyOnce sync.Once
	var readyErr error
	publishReady := func(state string) {
		readyOnce.Do(func() {
			if *readyFile == "" {
				return
			}
			readyErr = writeQueueExecReady(*readyFile, *runID, state)
			if readyErr != nil {
				cancelAcquire()
			}
		})
	}
	lease, err := cl.Acquire(acquireCtx, request, func(q wingwire.Queued) {
		publishReady("queued")
		fmt.Fprintf(os.Stderr, "queued for admission: position %d of %d\n", q.Position, q.QueueLength)
	})
	if err != nil {
		_ = cl.Close()
		if readyErr != nil {
			return fmt.Errorf("queue exec: publish readiness: %w", readyErr)
		}
		return fmt.Errorf("queue exec: admission: %w", err)
	}
	publishReady("granted")
	if readyErr != nil {
		_ = lease.Release()
		return fmt.Errorf("queue exec: publish readiness: %w", readyErr)
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		_ = lease.Release()
		return fmt.Errorf("queue exec: start command: %w", err)
	}

	cancelled := make(chan struct{}, 1)
	go lease.WatchControl(nil, func(wingwire.Cancel) {
		select {
		case cancelled <- struct{}{}:
		default:
		}
	})
	finished := make(chan error, 1)
	go func() { finished <- group.Finish(context.Background(), queueExecCleanupTimeout) }()

	var commandErr error
	select {
	case commandErr = <-finished:
	case <-cancelled:
		commandErr = terminateQueueExec(group)
	case <-ctx.Done():
		commandErr = errors.Join(ctx.Err(), terminateQueueExec(group))
	}
	releaseErr := lease.Release()
	if commandErr == nil && releaseErr != nil {
		return fmt.Errorf("queue exec: release admission: %w", releaseErr)
	}
	if commandErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(commandErr, &exitErr) && exitErr.ExitCode() > 0 {
		return exitError(exitErr.ExitCode(), fmt.Errorf("queue exec: command: %w", commandErr))
	}
	return fmt.Errorf("queue exec: command: %w", errors.Join(commandErr, releaseErr))
}

type queueExecReady struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
}

func writeQueueExecReady(path, runID, state string) (resultErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sparkwing-ready-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { resultErr = errors.Join(resultErr, os.Remove(tmpPath)) }()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := json.NewEncoder(tmp).Encode(queueExecReady{RunID: runID, State: state}); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func terminateQueueExec(group *procgroup.Group) error {
	ctx, cancel := context.WithTimeout(context.Background(), queueExecCleanupTimeout)
	defer cancel()
	return group.Terminate(ctx, time.Second)
}
