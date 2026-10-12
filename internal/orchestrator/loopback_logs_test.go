package orchestrator

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type loopbackRequest struct {
	path   string
	header http.Header
	body   []byte
}

func recordLoopback(t *testing.T, status int) (*loopbackLogs, chan loopbackRequest) {
	t.Helper()
	got := make(chan loopbackRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- loopbackRequest{path: r.URL.EscapedPath(), header: r.Header.Clone(), body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return newLoopbackLogs(srv.URL, nil, "node-token"), got
}

func TestLoopbackLogsAppendCarriesTheRangeFencesAndToken(t *testing.T) {
	logs, got := recordLoopback(t, http.StatusNoContent)
	ctx := store.WithNodeClaimFence(t.Context(), store.NodeClaimFence{
		HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 3,
	})
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: 5})
	ctx = withAppendRange(ctx, "stream-1", 4, 6)
	if err := logs.Append(ctx, "run 1", "build/linux", []byte("x\ny\nz\n")); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.path != "/api/v1/logs/run%201/build%2Flinux" || string(r.body) != "x\ny\nz\n" {
		t.Fatalf("append = %s %q", r.path, r.body)
	}
	for header, want := range map[string]string{
		"Authorization":               "Bearer node-token",
		authwire.NodeProtocolHeader:   authwire.NodeProtocolVersion,
		logseg.LogStreamHeader:        "stream-1",
		logseg.LogSeqHeader:           "4",
		logseg.LogSeqEndHeader:        "6",
		store.ClaimHolderHeader:       "holder",
		store.ClaimMembershipHeader:   "membership",
		store.ClaimReservationHeader:  "reservation",
		store.ClaimGenerationHeader:   "3",
		store.TriggerGenerationHeader: "5",
	} {
		if r.header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, r.header.Get(header), want)
		}
	}
}

func TestLoopbackLogsSealPostsTheWriterSeal(t *testing.T) {
	logs, got := recordLoopback(t, http.StatusNoContent)
	want := logseg.WriterSeal{Stream: "stream-1", FinalSeq: 9, Dropped: 2}
	if err := logs.Seal(t.Context(), "run-1", "build", want); err != nil {
		t.Fatal(err)
	}
	r := <-got
	var seal logseg.WriterSeal
	if err := json.Unmarshal(r.body, &seal); err != nil || seal != want || r.path != "/api/v1/logs/run-1/build/seal" {
		t.Fatalf("seal = %s %+v %v", r.path, seal, err)
	}
	if r.header.Get("Content-Type") != "application/json" || r.header.Get("Authorization") != "Bearer node-token" {
		t.Fatalf("seal headers = %v", r.header)
	}
}

// A 4xx is the endpoint refusing this node for good; a 5xx is worth retrying.
func TestLoopbackLogsRefusalIsOnlyA4xx(t *testing.T) {
	for status, refused := range map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusBadRequest:          true,
		http.StatusServiceUnavailable:  false,
		http.StatusInternalServerError: false,
	} {
		logs, _ := recordLoopback(t, status)
		err := logs.Append(t.Context(), "run-1", "build", []byte("x\n"))
		if err == nil || errors.Is(err, errLogRefused) != refused {
			t.Errorf("HTTP %d: err = %v, want refused=%v", status, err, refused)
		}
	}
}
