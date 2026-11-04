package client

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type retryFake struct {
	controllerwire.Controller
	refuse func(controllerwire.RetryRequest) error
	sent   []controllerwire.RetryRequest
}

func (f *retryFake) Retry(_ context.Context, _ string, input controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
	f.sent = append(f.sent, input)
	if err := f.refuse(input); err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	return controllerwire.CreateRunResponse{RunID: "new"}, nil
}

func TestRetryDropsReasonAndAgentForAnOlderController(t *testing.T) {
	legacy := &apierr.Error{Code: apierr.BadRequest, Message: legacyUnknownField}
	fake := &retryFake{refuse: func(input controllerwire.RetryRequest) error {
		if input.Reason != "" || input.Agent != "" {
			return legacy
		}
		return nil
	}}
	var notice bytes.Buffer
	input := controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "k", Reason: "flake: DNS timeout", Agent: "codex"}
	if _, err := Retry(t.Context(), fake, "source", input, &notice); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 || fake.sent[1].Reason != "" || fake.sent[1].Agent != "" || fake.sent[1].IdempotencyKey != "k" {
		t.Fatalf("sent %+v", fake.sent)
	}
	if !strings.Contains(notice.String(), "does not record retry reasons") || !strings.Contains(notice.String(), "flake: DNS timeout") {
		t.Fatalf("notice = %q", notice.String())
	}
}

func TestRetryFallbackLeavesOtherRefusalsAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input controllerwire.RetryRequest
		err   error
	}{
		{"no new fields", controllerwire.RetryRequest{Mode: controllerwire.RetryAll}, &apierr.Error{Code: apierr.BadRequest, Message: legacyUnknownField}},
		{"other field", controllerwire.RetryRequest{Reason: "r"}, &apierr.Error{Code: apierr.BadRequest, Message: "x", Details: map[string]any{apierr.DetailUnknownField: "mode"}}},
		{"reason refused", controllerwire.RetryRequest{Agent: "codex"}, &apierr.Error{Code: apierr.BadRequest, Message: "agent codex retried without a reason"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &retryFake{refuse: func(controllerwire.RetryRequest) error { return tc.err }}
			if _, err := Retry(t.Context(), fake, "source", tc.input, nil); !errors.Is(err, tc.err) || len(fake.sent) != 1 {
				t.Fatalf("err=%v sent=%d", err, len(fake.sent))
			}
		})
	}
}
