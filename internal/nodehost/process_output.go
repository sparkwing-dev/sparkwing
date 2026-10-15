package nodehost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const maxTextLine = 1 << 20

var sinkQueueBytes = 8 << 20

type processOutput struct {
	mu       sync.Mutex
	queues   []*sinkQueue
	grace    time.Duration
	holding  bool
	withheld string
	lost     int
	once     sync.Once
}

func newProcessOutput(cfg SessionConfig) *processOutput {
	output := &processOutput{grace: cfg.Grace, holding: cfg.HoldOutput}
	sink := cfg.Sink
	if sink == nil {
		sink = discardSink{}
	}
	for _, level := range []string{"", LevelInfo, LevelWarn} {
		queue := newSinkQueue(level, cfg.Masker)
		queue.held = cfg.HoldOutput
		output.queues = append(output.queues, queue)
		go queue.run(sink)
	}
	return output
}

func (s *processOutput) record(_ context.Context, record nodeproto.FrameLog) error {
	s.queues[0].push(sinkItem{rec: &record, size: len(record.TS) + len(record.Level) + len(record.Step) + len(record.Event) + len(record.Message) + len(record.Attrs)})
	return nil
}

func (s *processOutput) close() {
	s.once.Do(func() {
		s.noteWithheld()
		done := make(chan struct{})
		go func() {
			for _, queue := range s.queues {
				queue.close()
			}
			for _, queue := range s.queues {
				<-queue.done
			}
			close(done)
		}()
		timer := time.NewTimer(s.grace)
		defer timer.Stop()
		abandoned := false
		select {
		case <-done:
		case <-timer.C:
			abandoned = true
		}
		for _, queue := range s.queues {
			s.lost += queue.end(abandoned)
		}
	})
}

func (s *processOutput) release() {
	s.mu.Lock()
	holding := s.holding
	s.holding = false
	s.mu.Unlock()
	if !holding {
		return
	}
	marks := make([]chan struct{}, 0, len(s.queues))
	for _, q := range s.queues {
		marks = append(marks, q.release())
	}
	timer := time.NewTimer(s.grace)
	defer timer.Stop()
	for _, m := range marks {
		select {
		case <-m:
		case <-timer.C:
			return
		}
	}
}

func (s *processOutput) withhold(reason string) {
	s.mu.Lock()
	holding := s.holding
	s.holding = false
	if holding {
		s.withheld = reason
	}
	s.mu.Unlock()
	if !holding {
		return
	}
	for _, q := range s.queues {
		q.withhold()
	}
}

func (s *processOutput) noteWithheld() {
	s.withhold("the session closed before its output was released")
	s.mu.Lock()
	reason := s.withheld
	s.mu.Unlock()
	if reason == "" {
		return
	}
	n := 0
	for _, q := range s.queues {
		n += q.withheldLines()
	}
	if n > 0 {
		s.queues[0].notice(nodeproto.FrameLog{
			TS: time.Now().UTC().Format(time.RFC3339Nano), Level: LevelWarn,
			Message: fmt.Sprintf("sparkwing: output withheld: %s (%d lines)", reason, n),
		})
	}
}

func readQueuedText(f *os.File, q *sinkQueue) {
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	piece := false
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) && len(line) < maxTextLine {
			continue
		}
		if len(line) > 0 {
			end := len(line)
			if line[end-1] == '\n' {
				end--
				if end > 0 && line[end-1] == '\r' {
					end--
				}
			}
			piece = errors.Is(err, bufio.ErrBufferFull)
			cr := piece && line[end-1] == '\r'
			if cr {
				end--
			}
			q.push(sinkItem{text: string(line[:end]), piece: piece, size: end})
			line = line[:0]
			if cr {
				line = append(line, '\r')
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if piece {
				q.push(sinkItem{})
			}
			return
		}
	}
}

type sinkItem struct {
	text  string
	piece bool
	cut   bool
	rec   *nodeproto.FrameLog
	size  int
	mark  chan struct{}
}

type sinkQueue struct {
	level  string
	stream *secrets.Stream

	mu          sync.Mutex
	items       []sinkItem
	bytes       int
	dropped     int
	total       int
	closed      bool
	wake        chan struct{}
	done        chan struct{}
	held        bool
	withholding bool
	withheld    int
	abandoned   bool
}

func newSinkQueue(level string, masker *secrets.Masker) *sinkQueue {
	return &sinkQueue{level: level, stream: masker.Stream(), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (q *sinkQueue) push(item sinkItem) {
	item.size += 64
	q.mu.Lock()
	if q.withholding {
		q.withheld++
		q.mu.Unlock()
		return
	}
	if q.bytes > 0 && q.bytes+item.size > sinkQueueBytes {
		q.dropped++
		q.total++
		q.items[len(q.items)-1].cut = true
	} else {
		q.items = append(q.items, item)
		q.bytes += item.size
	}
	q.mu.Unlock()
	q.signal()
}

func (q *sinkQueue) release() chan struct{} {
	mark := make(chan struct{})
	q.mu.Lock()
	q.held = false
	q.items = append(q.items, sinkItem{mark: mark})
	q.mu.Unlock()
	q.signal()
	return mark
}

func (q *sinkQueue) withhold() {
	q.mu.Lock()
	q.withheld += len(q.items) + q.dropped
	q.total -= q.dropped
	q.items, q.bytes, q.dropped = nil, 0, 0
	q.held, q.withholding = false, true
	q.mu.Unlock()
	q.signal()
}

func (q *sinkQueue) withheldLines() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.withheld
}

func (q *sinkQueue) notice(rec nodeproto.FrameLog) {
	q.mu.Lock()
	q.items = append(q.items, sinkItem{rec: &rec})
	q.mu.Unlock()
	q.signal()
}

func (q *sinkQueue) end(abandoned bool) int {
	q.mu.Lock()
	defer q.signal()
	defer q.mu.Unlock()
	q.abandoned = abandoned
	n := q.total
	if abandoned {
		select {
		case <-q.done:
		default:
			n++
			for _, item := range q.items {
				if item.mark == nil {
					n++
				}
			}
		}
	}
	return n
}

func (q *sinkQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *sinkQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *sinkQueue) run(sink Sink) {
	defer close(q.done)
	for {
		q.mu.Lock()
		if q.abandoned {
			q.mu.Unlock()
			return
		}
		if len(q.items) == 0 || q.held {
			dropped, closed := q.dropped, q.closed && !q.held
			if q.held {
				dropped = 0
			}
			q.dropped -= dropped
			q.mu.Unlock()
			switch {
			case dropped > 0:
				sink.Record(nodeproto.FrameLog{
					TS: time.Now().UTC().Format(time.RFC3339Nano), Level: LevelWarn,
					Message: fmt.Sprintf("sparkwing: %d log lines dropped while the log sink was behind", dropped),
				})
			case closed:
				return
			default:
				<-q.wake
			}
			continue
		}
		item := q.items[0]
		q.items[0] = sinkItem{}
		q.items = q.items[1:]
		q.bytes -= item.size
		q.mu.Unlock()
		switch {
		case item.mark != nil:
			close(item.mark)
		case item.rec != nil:
			sink.Record(*item.rec)
		default:
			if text := q.stream.Mask(item.text, !item.piece); text != "" || !item.piece {
				sink.Text(q.level, text)
			}
		}
		if item.cut {
			q.stream.Drop()
		}
	}
}
