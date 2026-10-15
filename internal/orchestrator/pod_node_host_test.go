//go:build unix

package orchestrator

import (
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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

// A secret that straddles the session's split of a long plan line is masked
// whole, never printed as two fragments.
func TestPodPlanSink_MasksASecretAcrossASplitLine(t *testing.T) {
	const secret = "hunter2-straddles-the-split"
	masker := secrets.NewMasker()
	masker.Register(secret)
	var out strings.Builder
	sink, err := newPodPlanSink(t.Context(), "run-split", masker, &out)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("x", 1<<20-10) + secret[:10]
	sink.Text(nodehost.LevelInfo, head)
	sink.Text(nodehost.LevelInfo, secret[10:]+" tail")
	sink.end()
	if got := strings.ReplaceAll(out.String(), "\n", ""); strings.Contains(got, secret[:10]) || !strings.Contains(got, "*** tail") {
		t.Fatalf("plan output ends %q, want the split secret masked whole", got[max(0, len(got)-80):])
	}
}

// A plan whose stderr blocks past the session's grace ends with the head of a
// split secret still held and its rest still queued; ending the sink then
// prints neither piece.
func TestPodPlanSink_EndingDropsAHeldSecretAndItsLateRest(t *testing.T) {
	const secret = "hunter2-straddles-the-split"
	masker := secrets.NewMasker()
	masker.Register(secret)
	out := &blockingStderr{entered: make(chan struct{}), release: make(chan struct{})}
	sink, err := newPodPlanSink(t.Context(), "run-blocked", masker, out)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		sink.Text(nodehost.LevelInfo, strings.Repeat("x", 1<<20-10)+secret[:10])
		sink.Text(nodehost.LevelInfo, secret[10:]+" tail")
	}()
	<-out.entered
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sink.end()
	}()
	close(out.release)
	<-ended
	<-delivered
	got := out.String()
	if strings.Contains(got, secret[:10]) || strings.Contains(got, secret[10:]) {
		t.Fatalf("plan output after the end holds a piece of the secret: ...%q", got[max(0, len(got)-80):])
	}
}

// hack: holds its first write until release, standing in for a stalled container log.
type blockingStderr struct {
	mu      sync.Mutex
	buf     strings.Builder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingStderr) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *blockingStderr) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
