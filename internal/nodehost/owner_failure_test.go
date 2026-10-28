package nodehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestOwnerHostMasksEveryReturnedFailure(t *testing.T) {
	for _, stage := range []string{"acquire", "prepare", "plan", "accept", "schedule", "finish"} {
		t.Run(stage, func(t *testing.T) {
			f, host, input := newOwnerFixture(t, false)
			secret := "private-owner-credential"
			failure := &apierr.Error{Code: apierr.Unavailable, Message: secret, Hint: "retry " + secret, Details: map[string]any{"credential": secret}}
			host.Masker = testMasker(secret)
			switch stage {
			case "acquire":
				host.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
			case "prepare":
				host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) { return OwnerPlanning{}, failure }
			case "plan":
				prepare := host.Prepare
				host.Prepare = func(ctx context.Context, input RunInput, entry *source.Entry) (OwnerPlanning, error) {
					planning, err := prepare(ctx, input, entry)
					planning.Plan = func(context.Context) (json.RawMessage, error) { return nil, failure }
					return planning, err
				}
			case "accept":
				f.acceptErr = failure
			case "schedule":
				host.Project = func(store.RunSnapshot) (store.ScheduleSnapshot, error) { return store.ScheduleSnapshot{}, failure }
			case "finish":
				f.finishErr = failure
			}
			_, err := host.Run(t.Context(), input)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("owner exposed a %s failure: %v", stage, err)
			}
			var rendered *apierr.Error
			if !errors.Is(err, failure) || !errors.As(err, &rendered) || rendered.Code != failure.Code || strings.Contains(rendered.Message+rendered.Hint, secret) || rendered.Details["credential"] != "***" {
				t.Fatalf("owner lost the cause or exposed structured details: %v", err)
			}
		})
	}
}

func TestOwnerHostRequiresMaskingBeforeRenewal(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	host.Masker = nil
	if _, err := host.Run(t.Context(), input); err == nil || len(f.trace) != 0 {
		t.Fatalf("unmasked host started: trace=%v error=%v", f.trace, err)
	}
}

func TestOwnerHostAcquisitionReturningAfterCancellationCannotContinue(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		for _, cause := range []error{lease.ErrLeaseLost, lease.ErrSilent, &lease.CancelError{Reason: "cancel_requested"}} {
			t.Run(map[bool]string{false: "new", true: "resume"}[resumed]+"/"+cause.Error(), func(t *testing.T) {
				f, host, input := newOwnerFixture(t, resumed)
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
					cancel(cause)
					return &source.Entry{}, nil
				}
				completion, err := host.Run(ctx, input)
				if !errors.Is(err, cause) || completion.Outcome != "" || f.accepted.RunID != "" {
					t.Fatalf("acquisition continued after cancellation: completion=%+v error=%v", completion, err)
				}
				var cancelled *lease.CancelError
				if !resumed && errors.As(cause, &cancelled) {
					if f.finished.Transition.Reason != string(apierr.ReasonCancelled) || f.finished.Unit != input.Owner.Owner {
						t.Fatalf("unaccepted cancellation left its owner unfinished: %+v", f.finished)
					}
				} else if f.finished.Transition.RunID != "" {
					t.Fatalf("finished accepted work or lost authority: %+v", f.finished)
				}
			})
		}
	}
}

func TestOwnerHostPreparationFailureFinishesRunAndUnit(t *testing.T) {
	for _, stage := range []string{"acquire", "prepare", "plan"} {
		t.Run(stage, func(t *testing.T) {
			f, h, in := newOwnerFixture(t, false)
			const credential = "private-source-credential-91d2"
			if err := h.Masker.Register(credential); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("fetch failed with " + credential)
			switch stage {
			case "acquire":
				h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
			case "prepare":
				h.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) { return OwnerPlanning{}, failure }
			case "plan":
				prepare := h.Prepare
				h.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
					out, err := prepare(ctx, in, entry)
					out.Plan = func(context.Context) (json.RawMessage, error) { return nil, failure }
					return out, err
				}
			}
			_, err := h.Run(t.Context(), in)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if f.finished.Unit != in.Owner.Owner || f.finished.Transition.Cause != runstate.RunFinish {
				t.Fatalf("unfinished preparation failure: %+v", f.finished)
			}
			if text := f.finished.Transition.MaskedError; text != "fetch failed with ***" {
				t.Fatalf("persisted setup error %q, want the masked failure", text)
			}
			if f.accepted.RunID != "" {
				t.Fatal("accepted a failed preparation")
			}
		})
	}
}

func TestOwnerHostPreparationFinishRefusalIsLoud(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	failure, refused := errors.New("source unavailable"), errors.New("finish refused")
	f.finishErr = refused
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || !errors.Is(err, refused) {
		t.Fatalf("error = %v", err)
	}
}

func TestOwnerHostLostAuthorityDoesNotFinishPreparation(t *testing.T) {
	for _, cause := range []error{lease.ErrLeaseLost, lease.ErrSilent, context.Canceled} {
		f, h, in := newOwnerFixture(t, false)
		ctx, cancel := context.WithCancelCause(t.Context())
		h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { cancel(cause); return nil, cause }
		_, err := h.Run(ctx, in)
		if !errors.Is(err, cause) {
			t.Fatalf("error = %v", err)
		}
		if f.finished.Transition.RunID != "" {
			t.Fatalf("finished after authority ended: %+v", f.finished)
		}
	}
}

func TestOwnerHostCancelledPreparationFinishesWithCancellation(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	ctx, cancel := context.WithCancelCause(t.Context())
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		cancel(&lease.CancelError{Reason: "cancel_requested"})
		return nil, context.Cause(ctx)
	}
	_, err := h.Run(ctx, in)
	var cancelled *lease.CancelError
	if !errors.As(err, &cancelled) {
		t.Fatalf("error = %v", err)
	}
	if f.finished.Transition.Reason != string(apierr.ReasonCancelled) {
		t.Fatalf("finish = %+v", f.finished)
	}
}

func TestOwnerHostResumeAcquisitionFailureLeavesAcceptedWorkRecoverable(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	failure := errors.New("source unavailable")
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || f.finished.Transition.RunID != "" {
		t.Fatalf("error = %v finish = %+v", err, f.finished)
	}
}

func TestOwnerHostPolicyFailureCannotHideBehindTerminalNode(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	f.snapshot.Nodes = []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Spec: json.RawMessage(`{"id":"job"}`), Status: runstate.StatusPending}}
	failure := errors.New("policy write refused")
	h.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
		if s.Nodes[0].Outcome != "" {
			return store.ScheduleDecision{Outcome: "success"}
		}
		return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "job", Kind: store.DecisionReady}}}
	}
	setTestPolicies(&h, func(ctx context.Context, _ testPolicyName, _ EvaluationInput, row runstate.NodeRow, binding StorageBinding) (ownerLayerResult, error) {
		_, err := f.TransitionNode(ctx, store.DependencyNode{Ref: row.Ref, Outcome: runstate.NodeSkipped})
		return ownerLayerResult{}, errors.Join(failure, err)
	})
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || f.finished.Transition.RunID != "" {
		t.Fatalf("error = %v finish = %+v", err, f.finished)
	}
}

type echoingSourceAccess struct{ credential source.Credential }

func (a echoingSourceAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return a.credential, nil
}

func (echoingSourceAccess) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("no bundle")
}

// The plan host stores its preparation failure, so a fetch failure that echoes the source credential, raw or as
// the Basic header git sends, must reach the run masked.
func TestOwnerHostPreparationFailureMasksTheSourceCredential(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	directory, _ := sourcetest.Repository(t)
	sourcetest.Remote(t, map[string]string{sourceRemote: directory})
	credential := source.Credential{Kind: "https", Host: "8.8.8.8", Username: "x-access-token", Secret: "ghs_private-installation-token"}
	header := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + credential.Secret))
	h.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) {
		entry, err := source.PrepareCheckout(ctx, source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: sourceRemote, SHA: strings.Repeat("b", 40)}, source.Options{
			Base: procenv.Base{Ambient: sourcetest.Environment(t), Masker: h.Masker}, BuildEnvironment: sourcetest.BuildEnvironment,
			Workspace: t.TempDir(), Access: echoingSourceAccess{credential: credential},
		})
		if err == nil {
			_ = entry.Close()
			return nil, errors.New("fetched a commit the remote does not have")
		}
		return nil, fmt.Errorf("%w; remote replied to Authorization: Basic %s (token %s)", err, header, credential.Secret)
	}
	if _, err := h.Run(t.Context(), in); err == nil {
		t.Fatal("source fetch failure did not fail the run")
	}
	text := f.finished.Transition.MaskedError
	if !strings.Contains(text, "source git") || strings.Contains(text, credential.Secret) || strings.Contains(text, header) || !strings.Contains(text, "***") {
		t.Fatalf("stored failure does not mask the source credential: %q", text)
	}
}
