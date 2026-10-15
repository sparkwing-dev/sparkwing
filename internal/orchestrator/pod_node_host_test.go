//go:build unix

package orchestrator

import (
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"context"
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

// A log that lost lines seals as incomplete, so readers never take the prefix
// for the whole log.
func TestPodNodeLog_SealsIncompleteAfterALoss(t *testing.T) {
	up := &podLogUpload{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "acme", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "acme", RunID: "run", NodeID: "job", Attempt: 1, Generation: 1}}
	sink, err := logseg.NewSink(t.Context(), up, logseg.Options{Subject: subject, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
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
