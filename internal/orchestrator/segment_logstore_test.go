package orchestrator

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// A run on this machine whose state lives on the controller writes each
// node's log as segments from this process and reads it back through the
// controller, with no logs service in between.
func TestRemoteRunLogReachesTheControllerAsSegments(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	seg := controllerSegmentLogs(t.Context(), client.NewWithToken(ctrl.url, nil, tok),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if seg == nil {
		t.Fatal("the controller keeps segments, but no segment log store was opened")
	}
	defer seg.Close(t.Context())
	if !liveLogsRedundant(seg) {
		t.Fatal("a segment store pushes its own live view, so the always-on mirror must stay off")
	}

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

// A run whose state stays on this machine has no node rows on the
// controller to grant segments against, so its logs keep the logs service.
func TestLocalRunKeepsTheLogsServiceForControllerLogs(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	lookup := func(string) (string, string, error) { return ctrl.url, tok, nil }
	logs := backends.Spec{Type: backends.TypeController, Controller: "prod"}
	local := &backends.Spec{Type: backends.TypeSQLite, Path: "state.db"}
	got, err := openLogStoreFromSpec(t.Context(), logs, false, local, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*sparkwinglogs.Store); !ok {
		t.Fatalf("store = %T, want the logs service client", got)
	}
	remote := &backends.Spec{Type: backends.TypeController, Controller: "prod"}
	got, err = openLogStoreFromSpec(t.Context(), logs, true, remote, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*segmentLogStore); !ok {
		t.Fatalf("store = %T, want segments", got)
	}
	got.(*segmentLogStore).Close(t.Context())
}
