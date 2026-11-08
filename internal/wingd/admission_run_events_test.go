package wingd

import (
	"testing"
	"time"
)

type admissionEventRecorder struct {
	RunStore
	kinds []string
	data  []map[string]any
}

func (s *admissionEventRecorder) RecordAdmissionEvent(holder, kind string, data map[string]any) error {
	s.kinds = append(s.kinds, kind)
	s.data = append(s.data, data)
	return nil
}

func TestAdmissionDecisionsReachRunEventsWithoutJournal(t *testing.T) {
	recorder := &admissionEventRecorder{}
	d := &Daemon{cfg: Config{Runs: recorder, Now: func() time.Time { return time.Unix(30, 0) }}}
	c := &conn{runID: "run/node-host/am9i", startAt: time.Unix(10, 0)}
	d.recordJournal("queued", c, map[string]any{"blocking_reason": "needs 2 cores, 1 available", "position": 2})
	d.recordJournal("grant", c, map[string]any{"wait_ms": int64(20000)})
	if len(recorder.kinds) != 2 || recorder.kinds[0] != "queued" || recorder.kinds[1] != "grant" || recorder.data[0]["wait_ms"] != int64(20000) || recorder.data[0]["blocking_reason"] != "needs 2 cores, 1 available" || recorder.data[1]["wait_ms"] != int64(20000) {
		t.Fatalf("admission events lost: %+v %+v", recorder.kinds, recorder.data)
	}
}
