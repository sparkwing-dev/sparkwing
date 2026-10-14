package orchestrator

import (
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

// Plan output belongs to the run, so the renderer gets it with no job id.
func TestHostedPlanSink_GivesPlanOutputToTheRun(t *testing.T) {
	rec := &planSinkRecorder{}
	sink, err := newHostedPlanSink(t.Context(), "run-plan", secrets.NewMasker(), rec)
	if err != nil {
		t.Fatal(err)
	}
	sink.Text(nodehost.LevelInfo, "planning")
	if len(rec.recs) != 1 || rec.recs[0].JobID != "" || rec.recs[0].Msg != "planning" {
		t.Fatalf("plan records = %+v, want one with no job id", rec.recs)
	}
}
