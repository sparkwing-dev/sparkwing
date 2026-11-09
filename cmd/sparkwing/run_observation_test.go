package main

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLocalForegroundObservationRecoversAcrossControllerOutage(t *testing.T) {
	for name, failure := range map[string]error{
		"transport":   io.EOF,
		"unavailable": &apierr.Error{Code: hosterr.Unavailable, Message: "controller restarting"},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				available := time.Now().Add(6 * time.Second)
				var calls []string
				deps := localRuntimeFor(t, localTestAward(t), false, &calls)
				deps.interrupts = make(chan os.Signal)
				resolve := deps.resolve
				deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
					runtime, err := resolve(ctx, options)
					controller := runtime.controller.(localTestController)
					controller.read = func(context.Context, string) (controllerwire.RunDetail, error) {
						if time.Now().Before(available) {
							return controllerwire.RunDetail{}, failure
						}
						return controllerwire.RunDetail{ID: "local-run", Status: "success"}, nil
					}
					runtime.controller = controller
					first := true
					reader := fakeLogReader{read: func(_ context.Context, run string, query controllerwire.LogQuery) (controllerwire.LogPage, error) {
						if run != "local-run" {
							t.Errorf("log observation changed run to %q", run)
						}
						if first {
							first = false
							return controllerwire.LogPage{Records: []controllerwire.LogRecord{jobLogRecord(run, "before outage")}, NextCursor: "resume", Truncated: true}, nil
						}
						if query.Cursor != "resume" {
							t.Errorf("outage reset log cursor to %q", query.Cursor)
						}
						if time.Now().Before(available) {
							return controllerwire.LogPage{}, failure
						}
						after := jobLogRecord(run, "after outage")
						after.LineNo = 4
						return controllerwire.LogPage{Records: []controllerwire.LogRecord{after}, Complete: true}, nil
					}}
					runtime.follow = func(ctx context.Context, run, format string, out io.Writer, hosted <-chan struct{}) error {
						return observeRunLogs(ctx, logInvocation(t, "logs", "--run", run, "--follow", "-o", format), reader, nil, out, io.Discard, localFollowWait(hosted))
					}
					return runtime, err
				}
				var output bytes.Buffer
				err := runLocalCommand(t.Context(), "ci", runFlags{outputFormat: "json"}, []string{"--password", "private-value"}, &output, deps)
				if err != nil || !strings.Contains(output.String(), `"status":"success"`) || strings.Count(output.String(), "before outage") != 1 || strings.Count(output.String(), "after outage") != 1 {
					t.Fatalf("outage lost observation: output=%s err=%v", &output, err)
				}
				if !reflect.DeepEqual(calls, []string{"resolve", "prepare", "create", "close"}) {
					t.Fatalf("observation replayed intake or claimed work: %v", calls)
				}
			})
		})
	}
}

func TestLocalForegroundObservationKeepsReadRefusals(t *testing.T) {
	for _, code := range []apierr.Code{hosterr.Forbidden, hosterr.Unauthenticated, apierr.ProtocolError, hosterr.IntegrityFailure, apierr.NotFound} {
		t.Run(string(code), func(t *testing.T) {
			var calls []string
			deps := localRuntimeFor(t, localTestAward(t), false, &calls)
			deps.interrupts = make(chan os.Signal)
			resolve := deps.resolve
			reads := 0
			failure := &apierr.Error{Code: code, Message: "observation refused"}
			deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
				runtime, err := resolve(ctx, options)
				controller := runtime.controller.(localTestController)
				controller.read = func(context.Context, string) (controllerwire.RunDetail, error) {
					reads++
					return controllerwire.RunDetail{}, failure
				}
				runtime.controller = controller
				return runtime, err
			}
			var output bytes.Buffer
			err := runLocalCommand(t.Context(), "ci", runFlags{outputFormat: "json"}, []string{"--password", "private-value"}, &output, deps)
			var got *apierr.Error
			if !errors.As(err, &got) || got.Code != code || reads != 1 || strings.Contains(output.String(), `"event":"run_finish"`) {
				t.Fatalf("read refusal: output=%s reads=%d err=%v", &output, reads, err)
			}
		})
	}
}

func TestFollowingRunLogsKeepsReadRefusals(t *testing.T) {
	for _, code := range []apierr.Code{hosterr.Forbidden, hosterr.Unauthenticated, apierr.ProtocolError, hosterr.IntegrityFailure, apierr.NotFound} {
		t.Run(string(code), func(t *testing.T) {
			failure := &apierr.Error{Code: code, Message: "log read refused"}
			reads, waits := 0, 0
			reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
				reads++
				return controllerwire.LogPage{}, failure
			}}
			err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "local-run", "--follow"), reader, nil, io.Discard, io.Discard, func(context.Context, time.Duration) error {
				waits++
				return context.Canceled
			})
			if !errors.Is(err, failure) || reads != 1 || waits != 0 {
				t.Fatalf("log read refusal: reads=%d waits=%d err=%v", reads, waits, err)
			}
		})
	}
}

func TestRunLogOutageStopsOnCancellationAndPreservesOneShotReads(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "one shot", true: "cancelled follow"}[follow], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := &apierr.Error{Code: hosterr.Unavailable, Message: "controller restarting"}
			reads, waits := 0, 0
			reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
				reads++
				return controllerwire.LogPage{}, failure
			}}
			args := []string{"logs", "--run", "local-run"}
			if follow {
				args = append(args, "--follow")
			}
			err := observeRunLogs(ctx, logInvocation(t, args...), reader, nil, io.Discard, io.Discard, func(ctx context.Context, _ time.Duration) error {
				waits++
				cancel()
				return ctx.Err()
			})
			want := error(failure)
			if follow {
				want = context.Canceled
			}
			if !errors.Is(err, want) || reads != 1 || waits != map[bool]int{false: 0, true: 1}[follow] {
				t.Fatalf("outage cancellation: reads=%d waits=%d err=%v", reads, waits, err)
			}
		})
	}
}
