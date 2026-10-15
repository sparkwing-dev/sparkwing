//go:build unix

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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

func TestPodNodeLog_SealsIncompleteAfterALoss(t *testing.T) {
	up := &podLogUpload{}
	sink, err := logseg.NewSink(t.Context(), up, logseg.Options{Subject: blob.LogSubject{Kind: blob.LogJobAttempt, Team: "team", RunID: "run-s", Attempt: &runspec.AttemptIdentity{Team: "team", RunID: "run-s", NodeID: "a", Attempt: 1, Generation: 1}}, SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	nlog := &podNodeLog{sink: &nodeLogSink{sink: sink, logger: logger}, logger: logger}
	nlog.Emit(sparkwing.LogRecord{Msg: "kept"})
	nlog.RecordLoss(2)
	nlog.seal(t.Context(), false)
	if m := up.manifest; m.Status != logseg.StatusIncomplete || m.DroppedLines != 2 {
		t.Fatalf("manifest = %+v, want incomplete with 2 dropped lines", m)
	}
}

type podLogUpload struct{ manifest logseg.Manifest }

func (*podLogUpload) PutSegment(context.Context, logseg.Segment, []byte) error { return nil }

func (u *podLogUpload) PutManifest(_ context.Context, m logseg.Manifest) error {
	u.manifest = m
	return nil
}
