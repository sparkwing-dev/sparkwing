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
