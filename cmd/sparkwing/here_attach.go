package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

type hereObservation struct {
	host   func(context.Context) (nodehost.RunCompletion, error)
	follow func(context.Context) error
	cancel func(context.Context) error
}

func attachHereRun(ctx context.Context, options client.EngineOptions, run, output string, out io.Writer, host func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
	format, err := resolveTTYAwareOutput(output, "pipeline trigger")
	if err != nil {
		return nodehost.RunCompletion{}, err
	}
	controller, err := client.NewEngineClient(options)
	if err != nil {
		return nodehost.RunCompletion{}, incompleteHereHandoff(run, err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return observeHereRun(ctx, run, hereObservation{
		host: host,
		follow: func(ctx context.Context) error {
			return followControllerRunLogs(ctx, options, run, format, out)
		},
		cancel: func(ctx context.Context) error {
			_, writeErr := fmt.Fprintf(os.Stderr, "cancelling %s; interrupt again to stop hosting without waiting\n", run)
			_, err := controller.Cancel(ctx, run, controllerwire.CancelRequest{Reason: "interrupted by here host"})
			return errors.Join(writeErr, err)
		},
	}, signals)
}

type hereHostResult struct {
	completion nodehost.RunCompletion
	err        error
}

func observeHereRun(ctx context.Context, run string, observe hereObservation, signals <-chan os.Signal) (nodehost.RunCompletion, error) {
	if err := ctx.Err(); err != nil {
		return nodehost.RunCompletion{}, incompleteHereHandoff(run, err)
	}
	if observe.host == nil || observe.follow == nil {
		return nodehost.RunCompletion{}, incompleteHereHandoff(run, &apierr.Error{Code: apierr.Unavailable, Message: "here attachment requires a host and log reader"})
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	hosted := make(chan hereHostResult, 1)
	followed := make(chan error, 1)
	go func() {
		completion, err := observe.host(ctx)
		hosted <- hereHostResult{completion, err}
	}()
	go func() { followed <- observe.follow(ctx) }()
	var result hereHostResult
	var failures []error
	var cancelling chan error
	done := ctx.Done()
	requested := false
	for hosted != nil || followed != nil || cancelling != nil {
		select {
		case result = <-hosted:
			hosted = nil
			if result.err != nil {
				failures = append(failures, result.err)
				stop()
			}
		case err := <-followed:
			followed = nil
			if err != nil {
				failures = append(failures, err)
			}
		case err := <-cancelling:
			cancelling = nil
			if err != nil {
				failures = append(failures, err)
			}
		case _, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			if requested || hosted == nil {
				failures = append(failures, context.Canceled)
				stop()
				continue
			}
			requested = true
			if observe.cancel == nil {
				failures = append(failures, &apierr.Error{Code: apierr.Unavailable, Message: "here attachment has no cancellation client"})
				stop()
				continue
			}
			cancelling = make(chan error, 1)
			go func(result chan<- error) { result <- observe.cancel(ctx) }(cancelling)
		case <-done:
			failures = append(failures, ctx.Err())
			done = nil
			stop()
		}
	}
	if err := errors.Join(append(failures, ctx.Err())...); err != nil {
		return result.completion, incompleteHereHandoff(run, err)
	}
	return result.completion, nil
}
