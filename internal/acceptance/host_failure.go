package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckExecutorFailureRecords checks recovery data at retry and terminal persistence boundaries.
func CheckExecutorFailureRecords(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"before_refused", "verify_refused", "cleanup_refused", "output_refused", "retry", "success"} {
		t.Run(mode, func(t *testing.T) {
			var trace []string
			var processes []*executionSDK
			input, storage, host := executionHostFixture(t, mode, &trace, &processes)
			err := host.Execute(t.Context(), input)
			if mode == "before_refused" || mode == "cleanup_refused" || mode == "output_refused" {
				if err == nil {
					t.Fatal("lost execution failure")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			failures := 0
			for _, change := range storage.reports {
				result := change.Extra.Result
				if result == nil {
					continue
				}
				if result.Outcome != store.NodeFailed {
					if result.Failure != nil {
						t.Fatal("successful retry retained a failed attempt")
					}
					continue
				}
				failures++
				stage := "action"
				if mode == "verify_refused" {
					stage = "verify"
				}
				checkFailureRecord(t, *result, input.NodeID, stage, "private")
			}
			want := 1
			if mode == "success" {
				want = 0
			} else if mode == "retry" {
				want = 2
			} else if mode == "verify_refused" {
				want = 3
			}
			if failures != want {
				t.Fatalf("recorded %d failures, want %d", failures, want)
			}
		})
	}
}

// CheckExecutorFailureMask generates SDK error payloads before failed result persistence.
func CheckExecutorFailureMask(t *testing.T, value string, secretCode bool) {
	t.Helper()
	secret := "credential:" + value + ":end"
	var trace []string
	var processes []*executionSDK
	input, storage, host := executionHostFixture(t, "success", &trace, &processes, func(deps *nodehost.ExecutorDependencies) {
		deps.Mask = func(text string) string { return strings.ReplaceAll(text, secret, "***") }
		deps.Start = func(context.Context, nodehost.NodeInput, nodehost.ExecutorActivity) (nodehost.ExecutorProcess, error) {
			code := apierr.Code("future_sdk_error")
			if secretCode {
				code = apierr.Code(secret)
			}
			return nil, &apierr.Error{Code: code, Message: "failed " + secret + strings.Repeat("界", int(limits.ErrorBytes)) + " tail", Hint: secret, Details: map[string]any{"value": secret}}
		}
	})
	err := host.Execute(t.Context(), input)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("executor lost or exposed its failure")
	}
	var rendered *apierr.Error
	if !errors.As(err, &rendered) || rendered.Details != nil || strings.Contains(string(rendered.Code)+rendered.Message+rendered.Hint, secret) {
		t.Fatal("rendered host error contains an SDK secret payload")
	}
	result := storage.reports[len(storage.reports)-1].Extra.Result
	if result == nil {
		t.Fatal("failed startup has no result")
	}
	checkFailureRecord(t, *result, input.NodeID, "action", secret)
	if !secretCode && result.Failure.Error.Code != "future_sdk_error" {
		t.Fatal("additive error code was lost")
	}
}

func checkFailureRecord(t *testing.T, result store.NodeResult, job, stage, secret string) {
	t.Helper()
	failure := result.Failure
	if failure == nil || failure.Job != job || failure.Stage != stage || failure.Error == nil {
		t.Fatalf("missing bound failure: %+v", failure)
	}
	if failure.Error.Message != result.MaskedError || len(failure.Error.Message) > int(limits.ErrorBytes) || failure.Error.Code == "" || failure.Error.Hint != "" || failure.Error.Details != nil {
		t.Fatal("failure lost its masked text, bound or canonical error")
	}
	raw, err := json.Marshal(failure.Error)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apierr.DecodeInner(raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.MaskedError+result.Reason+string(failure.Error.Code)+failure.Error.Message, secret) {
		t.Fatal("persisted failure contains a secret")
	}
}
