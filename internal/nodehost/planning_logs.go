package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: retain the host's existing 8 MiB pre-describe queue and report any overflow.
const planningLogBufferBytes = 8 << 20

type planningLogs struct {
	mu      sync.Mutex
	masker  *secrets.Masker
	sink    func(context.Context, nodeproto.FrameLog) error
	known   bool
	debug   bool
	buffer  []nodeproto.FrameLog
	bytes   int
	dropped int
	err     error
}

func (l *planningLogs) write(ctx context.Context, record nodeproto.FrameLog) error {
	if record.Level == "debug" && !l.debug {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.known {
		return l.emit(ctx, record)
	}
	size := len(record.TS) + len(record.Level) + len(record.Step) + len(record.Event) + len(record.Message) + len(record.Attrs) + 64
	if size > planningLogBufferBytes-l.bytes {
		l.dropped++
		return nil
	}
	record.Attrs = append(json.RawMessage(nil), record.Attrs...)
	l.buffer = append(l.buffer, record)
	l.bytes += size
	return nil
}

func (l *planningLogs) release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.known = true
	for _, record := range l.buffer {
		if err := l.emit(ctx, record); err != nil {
			break
		}
	}
	l.buffer, l.bytes = nil, 0
	if l.err == nil && l.dropped > 0 {
		l.err = l.sink(ctx, nodeproto.FrameLog{Level: "warn", Message: fmt.Sprintf("sparkwing: %d log lines dropped before describe completed", l.dropped)})
	}
	l.dropped = 0
	return l.err
}

func (l *planningLogs) finish(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.known && len(l.buffer)+l.dropped > 0 {
		l.err = l.sink(ctx, nodeproto.FrameLog{Level: "warn", Message: fmt.Sprintf("sparkwing: describe output withheld because secret declarations were unavailable (%d lines)", len(l.buffer)+l.dropped)})
	}
	l.buffer, l.bytes, l.dropped = nil, 0, 0
	return l.err
}

func (l *planningLogs) emit(ctx context.Context, record nodeproto.FrameLog) error {
	record.TS, record.Level = l.masker.Mask(record.TS), l.masker.Mask(record.Level)
	record.Step, record.Event = l.masker.Mask(record.Step), l.masker.Mask(record.Event)
	record.Message = l.masker.Mask(record.Message)
	if len(record.Attrs) > 0 {
		if !json.Valid(record.Attrs) {
			l.err = executorError(apierr.ProtocolError, "SDK log attributes contain invalid JSON")
			return l.err
		}
		record.Attrs = l.masker.MaskJSON(record.Attrs)
		if !json.Valid(record.Attrs) {
			record.Attrs = json.RawMessage(`null`)
		}
	}
	l.err = l.sink(ctx, record)
	return l.err
}
