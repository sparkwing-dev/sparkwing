//go:build unix

package orchestrator

import (
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: each test links its own copy into its temp directory, which the build cache keeps cheap, so no
// binary outlives the test.
func podFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "podnodefixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/podnodefixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	return bin
}

// A log that lost lines seals as incomplete, so readers never take the prefix
// for the whole log.
func TestPodNodeLog_SealsIncompleteAfterALoss(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	nlog := &podNodeLog{sink: openNodeLogSink(t.Context(), client.NewWithToken(ctrl.url, nil, tok), "run-s", "a", logger), logger: logger}
	if nlog.sink == nil {
		t.Fatal("the controller issued no segment URLs")
	}
	nlog.Emit(sparkwing.LogRecord{Msg: "kept"})
	nlog.RecordLoss(2)
	nlog.seal(t.Context(), false)
	if m := ctrl.log(t).Manifest; m == nil || m.Status != logseg.StatusIncomplete || m.DroppedLines != 2 {
		t.Fatalf("manifest = %+v, want incomplete with 2 dropped lines", m)
	}
}
