package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runReadServer(t *testing.T, fail int32, status int) (*client.Client, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reads.Add(1) <= fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "run not found"})
			return
		}
		_ = json.NewEncoder(w).Encode(store.Run{ID: "run-1", PullRequest: &store.RunPullRequest{Number: 7, HeadRef: "feature"}})
	}))
	t.Cleanup(srv.Close)
	return client.NewWithToken(srv.URL, nil, "token"), &reads
}

func TestATriggerHostPlansFromTheRunRowAfterATransientReadFailure(t *testing.T) {
	c, _ := runReadServer(t, 1, http.StatusInternalServerError)
	rc, err := triggerRunContext(t.Context(), c, &store.Trigger{ID: "run-1", TriggerSource: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if rc.Trigger == nil || rc.Trigger.PullRequest == nil || rc.Trigger.PullRequest.Number != 7 {
		t.Fatalf("run context %+v, want the row's pull request, which every pod reads too", rc.Trigger)
	}
}

func TestATriggerHostRefusesToPlanWithoutItsRunRow(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: waits through 1.5s of the run read's real backoff")
	}
	c, reads := runReadServer(t, runReadAttempts, http.StatusInternalServerError)
	if _, err := triggerRunContext(t.Context(), c, &store.Trigger{ID: "run-1"}); err == nil {
		t.Fatal("planned from a partial run context; the pods would hash a different plan")
	}
	if got := reads.Load(); got != runReadAttempts {
		t.Errorf("read the run %d times, want %d", got, runReadAttempts)
	}
}

func TestATriggerHostPlansARunTheControllerHasNotRecordedFromItsTrigger(t *testing.T) {
	c, _ := runReadServer(t, runReadAttempts+1, http.StatusNotFound)
	rc, err := triggerRunContext(t.Context(), c, &store.Trigger{ID: "run-1", TriggerUser: "ada"})
	if err != nil || rc.Trigger == nil || rc.Trigger.User != "ada" {
		t.Fatalf("run context %+v err %v, want one from the trigger", rc.Trigger, err)
	}
}
