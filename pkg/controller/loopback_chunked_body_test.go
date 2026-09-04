package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// hack: wrapping the reader hides its length from the transport, which is how
// a test forces the chunked encoding the server sees as ContentLength == -1.
type unsizedLoopbackBody struct{ io.Reader }

// hack: the embedded interfaces are nil, so only the one method the claim
// route calls is implemented; anything else would panic rather than lie.
type leaseRecorderState struct {
	LoopbackState
	loopbackCoordination
	lease time.Duration
}

func (s *leaseRecorderState) ClaimSpecificTrigger(_ context.Context, id string, lease time.Duration) (*store.Trigger, error) {
	s.lease = lease
	expires := time.Now().UTC().Add(lease)
	return &store.Trigger{ID: id, Pipeline: "child", Status: "claimed", LeaseExpiresAt: &expires}, nil
}

func TestLoopbackClaimSpecificTrigger_ChunkedBodyKeepsTheLease(t *testing.T) {
	const token = "swl_chunked"
	state := &leaseRecorderState{}
	lb := NewLoopback(state, "run-loopback", token,
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	srv := httptest.NewServer(lb.Handler())
	defer srv.Close()

	body := `{"lease_nanos":` + strconv.FormatInt(int64(time.Hour), 10) + `}`
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/triggers/run-loopback/claim", unsizedLoopbackBody{strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST claim: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	var claimed store.Trigger
	if err := json.Unmarshal(raw, &claimed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.lease != time.Hour {
		t.Errorf("lease=%s, want the hour the chunked body asked for rather than the %s default",
			state.lease, store.DefaultLeaseDuration)
	}
}
