package orchestrator

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

// A run on this machine whose state lives on the controller writes each
// node's log as segments from this process and reads it back through the
// controller, with no logs service in between.
func TestRemoteRunLogReachesTheControllerAsSegments(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	seg, err := controllerSegmentLogs(t.Context(), client.NewWithToken(ctrl.url, nil, tok),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close(t.Context())

	nodeLog, err := NewLogStoreBackend(seg, nil).OpenNodeLog(t.Context(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"one", "two", "three"} {
		nodeLog.Log("info", msg)
	}
	if err := nodeLog.Close(); err != nil {
		t.Fatal(err)
	}

	l := ctrl.log(t)
	if l.Manifest == nil || l.Manifest.Status != logseg.StatusComplete || l.Manifest.Lines != 3 {
		t.Fatalf("manifest = %+v", l.Manifest)
	}
	got, err := seg.Read(t.Context(), "run-s", "a", storage.ReadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"two"`) || strings.Count(string(got), "\n") != 3 {
		t.Fatalf("read back %q", got)
	}
}

func TestControllerSegmentLogsRefusesAControllerThatKeepsNone(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"no segments": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"logs":{"segments":false}}`))
		},
		"capabilities unreadable": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if seg, err := controllerSegmentLogs(t.Context(), client.New(srv.URL, nil), nil); err == nil || seg != nil {
				t.Fatalf("store = %v, err = %v; want an error", seg, err)
			}
		})
	}
}
