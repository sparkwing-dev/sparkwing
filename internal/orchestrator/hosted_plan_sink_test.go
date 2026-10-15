package orchestrator

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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

// The hosted run's renderer and envelope log sit behind the run's masker, so
// a record no source masked, such as a plan row whose node id quotes a
// secret argument, is masked before it is shown or stored.
func TestHostedRendererMasksRecordsNoSourceMasked(t *testing.T) {
	const secret = "tenant-secret-4242"
	masker := secrets.NewMasker()
	masker.Register(secret)
	rec := &planSinkRecorder{}
	delegate := nodehost.MaskingLogger(rec, masker)
	emitHostedPlan(delegate, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "deploy-" + secret, Needs: []string{"build-" + secret}}}})
	delegate.Log("info", "summary "+secret)
	raw, err := json.Marshal(rec.recs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Count(string(raw), "***") < 3 {
		t.Fatalf("renderer got %s", raw)
	}
}
