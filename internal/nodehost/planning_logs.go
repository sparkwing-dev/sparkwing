package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const planningLogBufferBytes = 8 << 20

type planningOutput struct {
	frame       *nodeproto.FrameLog
	level, text string
	timestamp   string
	eof         bool
}

type planningLogs struct {
	mu             sync.Mutex
	masker         *secrets.Masker
	sink           func(context.Context, nodeproto.FrameLog) error
	fail           func(error)
	known, debug   bool
	buffer         []planningOutput
	presenter      *secrets.LogStreams
	bytes, dropped int
	err            error
	timer          *time.Timer
	closed         bool
}

func (l *planningLogs) write(ctx context.Context, record nodeproto.FrameLog) error {
	if record.Level == "debug" && !l.debug {
		return nil
	}
	record.Attrs = append(json.RawMessage(nil), record.Attrs...)
	return l.output(ctx, planningOutput{frame: &record}, len(record.TS)+len(record.Level)+len(record.Step)+len(record.Event)+len(record.Message)+len(record.Attrs)+len(record.Stream)+len(record.StreamData)+64)
}

func (l *planningLogs) text(ctx context.Context, level, text string, eof bool) error {
	return l.output(ctx, planningOutput{level: level, text: text, timestamp: time.Now().UTC().Format(time.RFC3339Nano), eof: eof}, len(text)+64)
}

func (l *planningLogs) output(ctx context.Context, output planningOutput, size int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.known {
		return l.emit(ctx, output)
	}
	if size > planningLogBufferBytes-l.bytes {
		l.dropped++
		return nil
	}
	l.buffer = append(l.buffer, output)
	l.bytes += size
	return nil
}

func (l *planningLogs) gate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.known = false
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

func (l *planningLogs) release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.known = true
	for _, output := range l.buffer {
		if err := l.emit(ctx, output); err != nil {
			break
		}
	}
	l.buffer, l.bytes = nil, 0
	if l.err == nil && l.dropped > 0 {
		l.err = l.sink(ctx, l.masker.Log(nodeproto.FrameLog{Level: "warn", Message: fmt.Sprintf("sparkwing: %d log lines dropped before describe completed", l.dropped)}))
	}
	l.dropped = 0
	return l.err
}

func (l *planningLogs) finish(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if l.err == nil && !l.known && len(l.buffer)+l.dropped > 0 {
		l.err = l.sink(ctx, l.masker.Log(nodeproto.FrameLog{Level: "warn", Message: fmt.Sprintf("sparkwing: output withheld because secret declarations were unavailable (%d lines)", len(l.buffer)+l.dropped)}))
	}
	if l.known && l.err == nil && l.presenter != nil {
		for _, frame := range l.presenter.Flush() {
			if l.err = l.sink(ctx, frame); l.err != nil {
				break
			}
		}
	}
	l.buffer, l.bytes, l.dropped = nil, 0, 0
	return l.err
}

func (l *planningLogs) emit(ctx context.Context, output planningOutput) error {
	if l.presenter == nil {
		l.presenter = secrets.NewLogStreams(l.masker)
	}
	var frames []nodeproto.FrameLog
	if output.frame != nil {
		var err error
		frames, err = l.presenter.Push(*output.frame)
		if err != nil {
			l.err = err
			return err
		}
	} else {
		frames = l.presenter.Text("raw:"+output.level, nodeproto.FrameLog{Level: output.level, TS: output.timestamp}, output.text, output.eof)
	}
	for _, frame := range frames {
		if l.err = l.sink(ctx, frame); l.err != nil {
			break
		}
	}
	if l.err == nil && !l.closed && l.presenter.Buffered() {
		if l.timer != nil {
			l.timer.Stop()
		}
		l.timer = time.AfterFunc(25*time.Millisecond, func() { l.idle(ctx) })
	}
	return l.err
}

func (l *planningLogs) idle(ctx context.Context) {
	l.mu.Lock()
	defer func() {
		err, fail := l.err, l.fail
		l.mu.Unlock()
		if err != nil && fail != nil {
			fail(err)
		}
	}()
	l.timer = nil
	if !l.known || l.closed || l.err != nil || l.presenter == nil {
		return
	}
	for _, frame := range l.presenter.Idle() {
		if l.err = l.sink(ctx, frame); l.err != nil {
			break
		}
	}
}

func (l *planningLogs) writer(ctx context.Context, level string) *planningLogWriter {
	return &planningLogWriter{logs: l, ctx: ctx, level: level}
}

type planningLogWriter struct {
	logs  *planningLogs
	ctx   context.Context
	level string
}

func (w *planningLogWriter) Write(p []byte) (int, error) {
	if err := w.logs.text(w.ctx, w.level, string(p), false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *planningLogWriter) close() error { return w.logs.text(w.ctx, w.level, "", true) }
