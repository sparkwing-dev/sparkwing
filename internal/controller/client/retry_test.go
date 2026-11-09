package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestRetryableTransportError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"missing_socket", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist}, true},
		{"missing_pipe", &os.PathError{Op: "connect", Path: "controller", Err: os.ErrNotExist}, true},
		{"lost_reply", io.ErrUnexpectedEOF, true},
		{"missing_file", os.ErrNotExist, false},
		{"socket_permission", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrPermission}, false},
		{"pipe_permission", &os.PathError{Op: "connect", Path: "controller", Err: os.ErrPermission}, false},
		{"authentication", &apierr.Error{Code: apierr.Unauthenticated}, false},
		{"policy", &apierr.Error{Code: apierr.Forbidden}, false},
		{"cancelled", context.Canceled, false},
		{"protocol", errors.New("invalid response"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryableTransportError(fmt.Errorf("controller request: %w", tc.err)); got != tc.want {
				t.Fatalf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMissingSocketDoesNotReplayClaim(t *testing.T) {
	calls := 0
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist}
	})
	_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
	if !RetryableTransportError(err) || calls != 1 {
		t.Fatalf("claim calls = %d, error = %v", calls, err)
	}
}

func TestParseRetryAfterClampsBeforeConverting(t *testing.T) {
	for _, raw := range []string{"9223372036", "92233720368547758", "99999999999999999999999"} {
		resp := &http.Response{Header: http.Header{"Retry-After": []string{raw}}}
		got, ok := parseRetryAfter(resp)
		if !ok || got != maxRetryAfter {
			t.Errorf("Retry-After %s = %s ok=%v, want %s", raw, got, ok, maxRetryAfter)
		}
	}
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"7"}}}
	if got, ok := parseRetryAfter(resp); !ok || got != 7*time.Second {
		t.Errorf("Retry-After 7 = %s ok=%v, want 7s", got, ok)
	}
}
