package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func hereObservationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHereAttachmentDrainsFinalLogs(t *testing.T) {
	hostFinished := make(chan struct{})
	logsFinished := false
	completion, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(context.Context) (nodehost.RunCompletion, error) {
			close(hostFinished)
			return nodehost.RunCompletion{Outcome: "success"}, nil
		},
		follow: func(ctx context.Context) error {
			<-hostFinished
			if err := ctx.Err(); err != nil {
				t.Errorf("host completion cancelled the final log flush: %v", err)
			}
			logsFinished = true
			return nil
		},
	}, nil)
	if err != nil || completion.Outcome != "success" || !logsFinished {
		t.Fatalf("completion=%+v logsFinished=%v error=%v", completion, logsFinished, err)
	}
}

func TestHereAttachmentFirstInterruptRequestsCancellationWithoutStoppingHost(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	cancelled := make(chan struct{})
	completion, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(ctx context.Context) (nodehost.RunCompletion, error) {
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Errorf("first interrupt stopped the host before cancellation: %v", ctx.Err())
			}
			return nodehost.RunCompletion{Outcome: "cancelled"}, nil
		},
		follow: func(ctx context.Context) error {
			select {
			case <-cancelled:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		cancel: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				t.Errorf("cancel request has a cancelled context: %v", err)
			}
			close(cancelled)
			return nil
		},
	}, signals)
	if err != nil || completion.Outcome != "cancelled" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
}

func TestHereAttachmentSecondInterruptJoinsEveryOperation(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	hostJoined, logsJoined, cancelJoined := false, false, false
	_, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(ctx context.Context) (nodehost.RunCompletion, error) {
			<-ctx.Done()
			hostJoined = true
			return nodehost.RunCompletion{}, ctx.Err()
		},
		follow: func(ctx context.Context) error {
			<-ctx.Done()
			logsJoined = true
			return ctx.Err()
		},
		cancel: func(ctx context.Context) error {
			signals <- os.Interrupt
			<-ctx.Done()
			cancelJoined = true
			return ctx.Err()
		},
	}, signals)
	if exitCodeFor(err) != 3 || !errors.Is(err, context.Canceled) || !hostJoined || !logsJoined || !cancelJoined {
		t.Fatalf("host=%v logs=%v cancel=%v error=%v", hostJoined, logsJoined, cancelJoined, err)
	}
}

func TestHereAttachmentLogFailureDoesNotAbandonHost(t *testing.T) {
	logFailure := errors.New("log persistence unavailable")
	logReturned := make(chan struct{})
	hostJoined := false
	completion, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(ctx context.Context) (nodehost.RunCompletion, error) {
			<-logReturned
			if err := ctx.Err(); err != nil {
				t.Errorf("observation failure abandoned execution: %v", err)
			}
			hostJoined = true
			return nodehost.RunCompletion{Outcome: "success"}, nil
		},
		follow: func(context.Context) error {
			close(logReturned)
			return logFailure
		},
	}, nil)
	if completion.Outcome != "success" || !hostJoined || !errors.Is(err, logFailure) || exitCodeFor(err) != 3 {
		t.Fatalf("completion=%+v host=%v error=%v", completion, hostJoined, err)
	}
}

func TestHereAttachmentHostFailureStopsAndJoinsLogReader(t *testing.T) {
	hostFailure := errors.New("host unavailable")
	joined := false
	_, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(context.Context) (nodehost.RunCompletion, error) { return nodehost.RunCompletion{}, hostFailure },
		follow: func(ctx context.Context) error {
			<-ctx.Done()
			joined = true
			return ctx.Err()
		},
	}, nil)
	if !errors.Is(err, hostFailure) || exitCodeFor(err) != 3 || !joined {
		t.Fatalf("logs=%v error=%v", joined, err)
	}
}

func TestHereAttachmentUsesPublicLogTransportAndSelectedOutput(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			options := client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "member-token", nil }, HTTP: &http.Client{Transport: logRoundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer member-token" || req.Method != "GET" || req.URL.Path != "/api/v1/runs/run/logs" {
					t.Errorf("public log request: %s %s", req.Method, req.URL)
				}
				body, err := json.Marshal(controllerwire.LogPage{Complete: true, Records: []controllerwire.LogRecord{jobLogRecord("run", "final line")}})
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}}
			var out bytes.Buffer
			completion, err := attachHereRun(hereObservationContext(t), options, "run", format, &out, func(context.Context) (nodehost.RunCompletion, error) {
				return nodehost.RunCompletion{Outcome: "success"}, nil
			})
			if err != nil || calls != 1 || completion.Outcome != "success" || !strings.Contains(out.String(), "final line") {
				t.Fatalf("calls=%d completion=%+v output=%q error=%v", calls, completion, &out, err)
			}
			if format == "json" && !json.Valid(bytes.TrimSpace(out.Bytes())) {
				t.Fatalf("invalid JSON log: %q", &out)
			}
		})
	}
}

func TestHereAttachmentCancellationFailureRemainsVisible(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	attempted := make(chan struct{})
	refusal := errors.New("cancel refused")
	completion, err := observeHereRun(hereObservationContext(t), "run", hereObservation{
		host: func(ctx context.Context) (nodehost.RunCompletion, error) {
			select {
			case <-attempted:
			case <-ctx.Done():
				return nodehost.RunCompletion{}, ctx.Err()
			}
			return nodehost.RunCompletion{Outcome: "success"}, nil
		},
		follow: func(context.Context) error { return nil },
		cancel: func(context.Context) error { close(attempted); return refusal },
	}, signals)
	if completion.Outcome != "success" || !errors.Is(err, refusal) || exitCodeFor(err) != 3 {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
}

func TestHereAttachmentKeepsDisplayAndExecutionAuthoritySeparate(t *testing.T) {
	options := client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "member-token", nil }}
	options.HTTP = &http.Client{Transport: logRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer member-token" || req.URL.Path != "/api/v1/runs/retry-run/logs" {
			t.Errorf("display transport borrowed execution authority: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"records":[],"next_cursor":"","truncated":false,"complete":true}`))}, nil
	})}
	credentials := hereCredentials{dir: t.TempDir()}
	award := hereAward(t)
	claimed, hosted, closed, attached := false, false, false, false
	var out bytes.Buffer
	deps := defaultHereDependencies("json")
	deps.credentials = func() (hereCredentials, error) { return credentials, nil }
	deps.attach = func(ctx context.Context, public client.EngineOptions, run string, host func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
		attached = true
		return attachHereRun(ctx, public, run, "json", &out, host)
	}
	deps.resolve = func(ctx context.Context, private client.EngineOptions) (hereRuntime, error) {
		bearer, err := private.Bearer(ctx)
		if err != nil || bearer != "retained-here-bearer" {
			t.Error("owner claim did not use retained authority")
		}
		return hereRuntime{
			controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claimed = true
				return controllerwire.ClaimResponse{Claim: &award}, nil
			}},
			bind: func(ctx context.Context, bound client.EngineOptions, _ controllerwire.ClaimedUnit) (hereHostSession, error) {
				bearer, err := bound.Bearer(ctx)
				if err != nil || bearer != award.Capability {
					t.Error("host did not use awarded authority")
				}
				return hereHostSession{host: hereTestHost(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
					hosted = true
					return nodehost.RunCompletion{Outcome: "success"}, nil
				}), close: func() error { closed = true; return nil }}, nil
			},
		}, nil
	}
	err := handoffHereRun(hereObservationContext(t), options, controllerwire.CreateRunResponse{RunID: "retry-run", HereBearer: "retained-here-bearer"}, deps)
	if err != nil || !claimed || !hosted || !closed || !attached {
		t.Fatalf("claim=%v host=%v close=%v attach=%v error=%v", claimed, hosted, closed, attached, err)
	}
}

func TestHereAttachmentCancelledBeforeStartHasNoSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := observeHereRun(ctx, "run", hereObservation{
		host: func(context.Context) (nodehost.RunCompletion, error) {
			t.Error("host started")
			return nodehost.RunCompletion{}, nil
		},
		follow: func(context.Context) error { t.Error("logs started"); return nil },
		cancel: func(context.Context) error { t.Error("cancel request sent"); return nil },
	}, nil)
	if !errors.Is(err, context.Canceled) || exitCodeFor(err) != 3 {
		t.Fatalf("error=%v", err)
	}
}
