package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type storedLog struct{ records []sparkwing.LogRecord }

func (l *storedLog) Log(level, msg string)        { l.Emit(sparkwing.LogRecord{Level: level, Msg: msg}) }
func (l *storedLog) Emit(rec sparkwing.LogRecord) { l.records = append(l.records, rec) }
func (l *storedLog) Close() error                 { return nil }

func TestHostedNodeLogShowsPrintedStdoutInTheCompactRenderer(t *testing.T) {
	var out bytes.Buffer
	stored := &storedLog{}
	log := hostedNodeLog{NodeLog: stored, delegate: newCompactRunRenderer(&out)}

	log.EmitText(sparkwing.LogRecord{Level: "info", JobID: "build", Msg: "printed line"})
	log.Emit(sparkwing.LogRecord{Level: "info", JobID: "build", Msg: "sdk info"})

	if !strings.Contains(out.String(), "printed line") {
		t.Errorf("compact output %q lacks the node's stdout; the old local runner showed it at warn", out.String())
	}
	if strings.Contains(out.String(), "sdk info") {
		t.Errorf("compact output %q shows an SDK info record the old path hid", out.String())
	}
	if len(stored.records) != 2 || stored.records[0].Level != "info" {
		t.Errorf("stored %v, want both records with stdout kept at info", stored.records)
	}
}
