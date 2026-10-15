package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDetachedIntakeUsesSealsWithoutClaimingWork(t *testing.T) {
	request := initialTriggerRequest(false)
	request.Request.Origin = runspec.OriginLocal
	request.IdempotencyKey = "selected-intent"
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request.Request.Env, err = snapshots.Capture(t.Context(), []string{"PATH=/submit/bin", "DOCKER_HOST=unix:///submit.sock"})
	if err != nil {
		t.Fatal(err)
	}
	var received controllerwire.CreateRunRequest
	handle := filepath.Join(t.TempDir(), "run.json")
	controller := initialTriggerController{create: func(ctx context.Context, input controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
		if _, err := os.Stat(handle); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("handle published before intake: %v", err)
		}
		received = input
		return initialTriggerResponse(t, input), nil
	}}
	result, err := submitDetached(t.Context(), controller, request, handle, "request-observation")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, received) || result.RunID != "initial-run" || result.Status != "pending" || result.RequestID != "request-observation" || result.AlreadySubmitted {
		t.Fatalf("detached intake = %+v", result)
	}
	data, err := os.ReadFile(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), result.RunID) || strings.Contains(string(data), "submit.sock") {
		t.Fatal("handle lost run identity or exposed environment values")
	}
	for range 2 {
		captured, err := snapshots.Load(t.Context(), request.Request.Env)
		if err != nil || !reflect.DeepEqual(captured, []string{"DOCKER_HOST=unix:///submit.sock", "PATH=/submit/bin"}) {
			t.Fatalf("replacement host snapshot = %v, %v", captured, err)
		}
	}
}

func TestDetachedReplayUsesPersistedStatus(t *testing.T) {
	request := initialTriggerRequest(false)
	request.IdempotencyKey = "stable-intent"
	controller := initialTriggerController{
		create: func(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			response := initialTriggerResponse(t, request)
			response.Replayed = true
			return response, nil
		},
		read: func(ctx context.Context, run string) (controllerwire.RunDetail, error) {
			return controllerwire.RunDetail{ID: run, Status: "success"}, nil
		},
	}
	result, err := submitDetached(t.Context(), controller, request, "", "observation")
	if err != nil || result.Status != "success" || !result.AlreadySubmitted || result.IdempotencyKey != "stable-intent" {
		t.Fatalf("replay = %+v, %v", result, err)
	}
}

func TestDetachedIntakeFailureNeverPublishesHandle(t *testing.T) {
	for _, mode := range []string{"refused", "missing_run", "invalid_seal", "foreign_run", "replay_missing_status"} {
		t.Run(mode, func(t *testing.T) {
			request := initialTriggerRequest(false)
			handle := filepath.Join(t.TempDir(), "run.json")
			controller := initialTriggerController{create: func(ctx context.Context, input controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				response := initialTriggerResponse(t, input)
				switch mode {
				case "refused":
					return controllerwire.CreateRunResponse{}, &apierr.Error{Code: apierr.InvalidArgs, Message: "refused"}
				case "missing_run":
					response.RunID = ""
				case "invalid_seal":
					response.Request.Hash = "forged"
				case "foreign_run":
					response.RunID = "other"
				case "replay_missing_status":
					response.Replayed = true
				}
				return response, nil
			}, read: func(context.Context, string) (controllerwire.RunDetail, error) {
				return controllerwire.RunDetail{}, nil
			}}
			if _, err := submitDetached(t.Context(), controller, request, handle, ""); err == nil {
				t.Fatal("invalid intake succeeded")
			}
			if _, err := os.Stat(handle); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid intake published handle: %v", err)
			}
		})
	}
}

func FuzzDetachedIntakeDiagnosticsNeverExposeArguments(f *testing.F) {
	f.Add([]byte("private input"), true)
	f.Add([]byte{0, '\n', 255}, false)
	f.Fuzz(func(t *testing.T, data []byte, canonical bool) {
		secret := "private-argument-" + hex.EncodeToString(data)
		if len(secret) > 4096 {
			t.Skip()
		}
		request := initialTriggerRequest(false)
		request.Request.RequestedArgs = map[string]string{"password": secret}
		cause := error(errors.New("intake failed: " + secret))
		if canonical {
			cause = &apierr.Error{Code: apierr.InvalidArgs, Message: "intake failed: " + secret}
		}
		controller := initialTriggerController{create: func(context.Context, controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
			return controllerwire.CreateRunResponse{}, cause
		}}
		_, err := submitDetached(t.Context(), controller, request, "", "")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("intake diagnostic exposed an argument")
		}
		var structured *apierr.Error
		if errors.As(err, &structured) {
			data, marshalErr := json.Marshal(structured)
			if marshalErr != nil || strings.Contains(string(data), secret) {
				t.Fatal("structured intake diagnostic exposed an argument")
			}
		}
	})
}

func TestScheduledIntakeDelegatesSelectionAndDeduplicatesDueInstant(t *testing.T) {
	due := time.Date(2026, 10, 15, 9, 0, 0, 123, time.UTC)
	manager := &scheduleFixture{rows: map[string]controllerwire.DetailView{"schedule": {Schedule: controllerwire.ScheduleView{ID: "schedule"}}}}
	for _, at := range []time.Time{due, due.In(time.FixedZone("other", -6*60*60)), due.Add(time.Minute)} {
		run, err := launchScheduledRun(t.Context(), manager, "schedule", at)
		if err != nil || run != "run-schedule" {
			t.Fatalf("scheduled intake = %q, %v", run, err)
		}
	}
	if len(manager.keys) != 3 || manager.keys[0] == "" || manager.keys[0] != manager.keys[1] || manager.keys[0] == manager.keys[2] {
		t.Fatalf("due-instant keys = %v", manager.keys)
	}
}
