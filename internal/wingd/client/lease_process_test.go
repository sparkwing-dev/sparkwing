package client

import (
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestLeaseTrackProcessSendsTheChildProcessIdentity(t *testing.T) {
	home := shortHome(t)
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			PID          int    `json:"pid"`
			ProcessBirth string `json:"process_birth"`
		}
		if r.Method != http.MethodPost || r.URL.Path != admissionPath+"/requests/job/process" {
			t.Errorf("process registration route: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.PID != os.Getpid() || input.ProcessBirth != birth {
			t.Errorf("process identity = %+v, error=%v", input, err)
		}
		called.Store(true)
		writeHTTPFixture(w, map[string]string{"state": "granted"})
	}))
	held := &Lease{cl: fixtureClient(t, home), id: "job"}
	if err := held.TrackProcess(t.Context(), os.Getpid()); err != nil || !called.Load() {
		t.Fatalf("register process called=%t error=%v", called.Load(), err)
	}
}
