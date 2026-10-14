package orchestrator

import (
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type planSinkRecorder struct {
	mu   sync.Mutex
	recs []sparkwing.LogRecord
}

func (r *planSinkRecorder) Log(level, msg string) {
	r.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (r *planSinkRecorder) Emit(rec sparkwing.LogRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

// A secret that straddles the session's split of a long plan line on the
// local host is masked whole, never handed to the renderer as two fragments.
func TestHostedPlanSink_MasksASecretAcrossASplitLine(t *testing.T) {
	const secret = "hunter2-straddles-the-split"
	masker := secrets.NewMasker()
	masker.Register(secret)
	rec := &planSinkRecorder{}
	sink, err := newHostedPlanSink(t.Context(), "run-split", masker, secrets.MaskingLogger(rec, masker))
	if err != nil {
		t.Fatal(err)
	}
	sink.Text(nodehost.LevelInfo, strings.Repeat("x", 1<<20-10)+secret[:10])
	sink.Text(nodehost.LevelInfo, secret[10:]+" tail")
	sink.Flush()
	var got strings.Builder
	for _, r := range rec.recs {
		if r.JobID != "" {
			t.Errorf("plan record carries job %q; plan output belongs to the run", r.JobID)
		}
		got.WriteString(r.Msg)
	}
	if out := got.String(); strings.Contains(out, secret[:10]) || !strings.Contains(out, "*** tail") {
		t.Fatalf("plan output ends %q, want the split secret masked whole", out[max(0, len(out)-80):])
	}
}
