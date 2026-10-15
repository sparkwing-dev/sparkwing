package orchestrator

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/logpretty"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type PrettyRenderer = logpretty.PrettyRenderer

func NewPrettyRenderer() *PrettyRenderer { return logpretty.NewPrettyRenderer() }

func NewPrettyRendererTo(w io.Writer, useColor bool) *PrettyRenderer {
	return logpretty.NewPrettyRendererTo(w, useColor)
}

type QuietRenderer = logpretty.QuietRenderer

func NewQuietRenderer() *QuietRenderer { return logpretty.NewQuietRenderer() }

func StripANSI(s string) string { return logpretty.StripANSI(s) }

type JSONRenderer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{enc: json.NewEncoder(os.Stdout)}
}

func (j *JSONRenderer) Log(level, msg string) {
	j.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (j *JSONRenderer) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.enc.Encode(&rec)
}
