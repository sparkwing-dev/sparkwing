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
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for route, track := range map[string]func(*Lease) error{
		"process": func(l *Lease) error { return l.TrackProcess(t.Context(), os.Getpid()) },
		"command": func(l *Lease) error { return l.TrackCommand(t.Context(), os.Getpid(), birth) },
	} {
		t.Run(route, func(t *testing.T) {
			home := shortHome(t)
			var called atomic.Bool
			serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					PID          int    `json:"pid"`
					ProcessBirth string `json:"process_birth"`
				}
				if r.Method != http.MethodPost || r.URL.Path != admissionPath+"/requests/job/"+route {
					t.Errorf("registration route: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.PID != os.Getpid() || input.ProcessBirth != birth {
					t.Errorf("process identity = %+v, error=%v", input, err)
				}
				called.Store(true)
				writeHTTPFixture(w, map[string]string{"state": "granted"})
			}))
			if err := track(&Lease{cl: fixtureClient(t, home), id: "job"}); err != nil || !called.Load() {
				t.Fatalf("register called=%t error=%v", called.Load(), err)
			}
		})
	}
}
