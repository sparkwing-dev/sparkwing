package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunObservationsStopPollersOnARefusedRead(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&apierr.Error{Code: apierr.NotFound, Message: "resource not found"}, exitRefused},
		{&apierr.Error{Code: apierr.Forbidden, Message: "credential does not authorize this operation"}, exitRefused},
		{&apierr.Error{Code: apierr.Unauthenticated, Message: "no live credential"}, exitRefused},
		{&apierr.Error{Code: apierr.Unavailable, Message: "controller restarting"}, 3},
		{errors.New("connection reset"), 3},
	} {
		var out bytes.Buffer
		status := observeRunStatus(t.Context(), statusInvocation(t), &statusReader{err: tc.err}, &out, nil)
		if got := exitCodeFor(status); got != tc.want {
			t.Errorf("runs status on %v exits %d, want %d", tc.err, got, tc.want)
		}
		reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
			return controllerwire.LogPage{}, tc.err
		}}
		logs := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "nosuch"), reader, nil, &out, &out, nil)
		if got := exitCodeFor(logs); got != tc.want {
			t.Errorf("runs logs on %v exits %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestRunListStopsPollersOnARefusalInAnyRead(t *testing.T) {
	refused := &apierr.Error{Code: apierr.Forbidden, Message: "credential revoked"}
	failedRun := listedRun("r1", 7)
	failedRun.Status = "failed"
	for name, tc := range map[string]struct {
		args   []string
		reader listingReader
	}{
		"second page of --by-pipeline": {
			args: []string{"--by-pipeline", "-o", "json"},
			reader: listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
				if q.Cursor == "" {
					return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("r2", 8)}, NextCursor: "next", Truncated: true}, nil
				}
				return controllerwire.RunPage{}, refused
			}},
		},
		"steps of --group-by step": {
			args: []string{"--status", "failed", "--group-by", "step", "-o", "json"},
			reader: listingReader{
				list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
					return controllerwire.RunPage{Runs: []controllerwire.RunDetail{failedRun}}, nil
				},
				steps: func(context.Context, string) (controllerwire.RunSteps, error) {
					return controllerwire.RunSteps{}, refused
				},
			},
		},
	} {
		var out bytes.Buffer
		err := observeRunList(t.Context(), listingInvocation(t, tc.args...), tc.reader, &out, &out, nil)
		if got := exitCodeFor(err); got != exitRefused {
			t.Errorf("%s: exit %d (%v), want %d", name, got, err, exitRefused)
		}
	}
}
