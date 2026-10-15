package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLogSinkReportsProgressPerRecord(t *testing.T) {
	ticks := 0
	sink, err := NewLogSink(t.Context(), LogSinkConfig{
		RunID: testRunID, NodeID: "build", Log: discardLog{}, State: noState{}, Masker: secrets.NewMasker(),
		Progress: func() { ticks++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	sink.Text(LevelInfo, "compiling")
	sink.Record(nodeproto.FrameLog{Level: "info", Message: "linking"})
	if ticks != 2 {
		t.Errorf("progress ticks %d, want one per stored record", ticks)
	}
}

type discardLog struct{}

func (discardLog) Emit(sparkwing.LogRecord) {}

type noState struct{}

func (noState) AppendNodeAnnotation(context.Context, string, string, string) error { return nil }

func (noState) AppendStepAnnotation(context.Context, string, string, string, string) error {
	return nil
}

func (noState) SetNodeSummary(context.Context, string, string, string) error { return nil }

func (noState) SetStepSummary(context.Context, string, string, string, string) error { return nil }
