package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestEngineClientResponseLimit(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		size          int64
		contentLength int64
		oversized     bool
	}{
		{"at limit", http.StatusOK, maxEngineResponseBytes, -1, false},
		{"oversized success", http.StatusOK, maxEngineResponseBytes + 1, -1, true},
		{"unending error", http.StatusServiceUnavailable, 1 << 40, -1, true},
		{"false content length", http.StatusNoContent, 1 << 40, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const envelope = `{"claim":null}`
			body := &countedResponseBody{Reader: io.MultiReader(strings.NewReader(envelope), io.LimitReader(responsePadding{}, test.size-int64(len(envelope))))}
			calls := 0
			c := testEngine(t, func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: body, ContentLength: test.contentLength}, nil
			})
			result, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
			if test.oversized {
				if err == nil || !strings.Contains(err.Error(), "controller response exceeds 67108864 bytes") {
					t.Fatalf("oversized response: %v", err)
				}
			} else if err != nil || result.Claim != nil {
				t.Fatalf("boundary response: %+v, %v", result, err)
			}
			if body.read != min(test.size, maxEngineResponseBytes+1) || !body.closed || calls != 1 {
				t.Fatalf("read=%d closed=%v requests=%d", body.read, body.closed, calls)
			}
		})
	}
}

type responsePadding struct{}

func (responsePadding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type countedResponseBody struct {
	io.Reader
	read   int64
	closed bool
}

func (b *countedResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += int64(n)
	return n, err
}

func (b *countedResponseBody) Close() error { b.closed = true; return nil }

func TestEngineClientLargeSnapshot(t *testing.T) {
	// safety: a scheduling snapshot can exceed the individual plan body limit.
	plan := json.RawMessage(`{"jobs":[{"id":"large","steps":[{"id":"run"}],"env":{"LARGE":{"value":"` + strings.Repeat("x", store.MaxPlanBytes-len(`{"jobs":[{"id":"large","steps":[{"id":"run"}],"env":{"LARGE":{"value":""}}}]}`)) + `"}}}]}`)
	snapshot := controllerwire.ScheduleSnapshot{RunID: "r1", Expansions: []controllerwire.AcceptedExpansion{{Body: plan}}}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(http.StatusOK, string(data)), nil
	})
	got, err := c.ReadSnapshot(t.Context(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Expansions) != 1 || string(got.Expansions[0].Body) != string(plan) {
		t.Fatal("large accepted expansion was not preserved")
	}
}
