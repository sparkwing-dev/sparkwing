package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A child that retries a refused append keeps that line's number; one that
// moves on to other bytes gave the line up, and the seal counts it dropped.
func TestBrokerSealCountsLinesTheChildGaveUp(t *testing.T) {
	srv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	var refuse atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() && r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/seal") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	fence := store.NodeClaimFence{HolderID: "h", MembershipID: "m", ReservationID: "r", ClaimGeneration: 1}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	run := func(t *testing.T, runID string, bodies []string, refused map[int]bool) logs.StreamReport {
		t.Helper()
		broker, err := startRemoteExecutionBroker(upstream.URL, upstream.URL, "", runID, "build", fence, nil, logger)
		if err != nil {
			t.Fatal(err)
		}
		defer broker.Close()
		for i, body := range bodies {
			refuse.Store(refused[i])
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
				broker.URL()+"/api/v1/logs/"+runID+"/build", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+broker.capability)
			req.Header.Set(store.AttemptOrdinalHeader, "1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
		refuse.Store(false)
		broker.sealChildLogs(context.Background(), assistedChildOutcome{}, nil, logger)
		r, err := logs.NewClient(upstream.URL, nil).ReadSeals(context.Background(), runID, "build")
		if err != nil || len(r.Streams) != 1 || !r.Streams[0].Sealed {
			t.Fatalf("report = %+v, %v", r, err)
		}
		return r.Streams[0]
	}

	// Negative control: a refused line the child retries arrives, so nothing is lost.
	retried := run(t, "run-retried", []string{"one\n", "two\n", "two\n", "three\n"}, map[int]bool{1: true})
	if retried.Seal.Dropped != 0 || retried.Seal.FinalSeq != 3 || retried.Missing != 0 {
		t.Fatalf("retried stream = %+v seal %+v", retried, retried.Seal)
	}
	gaveUp := run(t, "run-gave-up", []string{"one\n", "two\n", "three\n"}, map[int]bool{1: true})
	if gaveUp.Seal.Dropped != 1 || gaveUp.Seal.FinalSeq != 2 {
		t.Fatalf("given-up stream = %+v seal %+v", gaveUp, gaveUp.Seal)
	}
}
