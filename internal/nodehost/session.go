package nodehost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

var (
	// ErrClosed fails a call made after, or still waiting at, [Session.Close].
	ErrClosed = errors.New("nodehost: sdk session closed")
	// ErrBusy refuses a request the protocol forbids alongside those in
	// flight: describe and plan go alone, and run_node never overlaps
	// another run_node or a run_step.
	ErrBusy = errors.New("nodehost: request not allowed while others are in flight")
)

// The text-record levels of the SDK process's stdout and stderr.
const (
	LevelInfo = "info"
	LevelWarn = "warn"
)

// safety: bounds the memory one unterminated stdout or stderr line can hold;
// a longer line reaches the sink in pieces of this size.
const maxTextLine = 1 << 20

// safety: bounds the wait for the session's members to die after the kill, so
// a process stuck in the kernel cannot hold the session open.
var reapTimeout = 10 * time.Second

// safety: bounds what a slow sink can make one stream hold; past it the
// stream's lines are dropped and counted rather than stalling the pipe.
var sinkQueueBytes = 8 << 20

// Sink receives everything an SDK process writes other than replies. The
// session calls it from several goroutines at once. Masking and storage are
// the sink's job.
type Sink interface {
	// Text receives one stdout line at LevelInfo or stderr line at LevelWarn,
	// verbatim apart from its line ending.
	Text(level, line string)
	// Record receives a frame-pipe log record. Step is kept only while a
	// run_step for that step is in flight.
	Record(rec nodeproto.LogRecord)
}

// SessionConfig says how to start an SDK process.
type SessionConfig struct {
	// Entry is the program run as "<Entry> --sw-node-protocol".
	Entry string
	// Dir is the repository root.
	Dir string
	// Env is the process's whole environment, which the caller sets on
	// purpose: a local run passes the user's, a pod the minimal set. Nil is
	// [MinimalEnv], so the engine's credentials never reach pipeline code
	// implicitly.
	Env  []string
	Sink Sink
	// Grace is how long the process gets to exit after its request pipe
	// closes, and between SIGTERM and SIGKILL. Zero means
	// procgroup.DefaultTerminationGrace.
	Grace time.Duration
	// Started, when set, is called with each SDK process's pid as soon as it
	// starts, before any request, so a caller can record the process session
	// to reap if this host dies without reaping it.
	Started func(pid int)
}

type sessionProcess interface {
	LeaderExited() <-chan struct{}
	Finish(ctx context.Context, grace time.Duration) error
	Terminate(ctx context.Context, grace time.Duration) error
}

type call struct {
	op    nodeproto.Op
	step  string
	reply chan nodeproto.Reply
}

// Session is one SDK process speaking the node protocol: requests go out on
// its request pipe and replies and log records come back on its frame pipe.
// A failure ends the session; every pending and later call returns it.
type Session struct {
	cfg      SessionConfig
	cmd      *exec.Cmd
	proc     sessionProcess
	sampler  *UsageSampler
	requests *os.File
	writer   *nodeproto.Writer
	tracker  nodeproto.Tracker

	mu       sync.Mutex
	inflight map[string]call
	err      error
	// safety: a protocol break found while Close drains the frame pipe still
	// taints the replies before it, so Close reports it.
	closingErr error
	failed     chan struct{}

	reads     []*os.File
	queues    []*sinkQueue
	streams   sync.WaitGroup
	frameEOF  chan struct{}
	watchDone chan struct{}
	reapErr   error

	closeOnce sync.Once
	usage     Usage
	closeErr  error
	lost      int
}

// StartSession starts cfg.Entry as the leader of a new process session with
// stdin on the null device. Cancelling ctx kills the whole session. The
// caller must call [Session.Close].
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if cfg.Grace <= 0 {
		cfg.Grace = procgroup.DefaultTerminationGrace
	}
	var parent, child []*os.File
	closeAll := func(files []*os.File) {
		for _, f := range files {
			_ = f.Close()
		}
	}
	// safety: the child gets the request pipe's read end and the other three pipes' write ends.
	for range 4 {
		r, w, err := os.Pipe()
		if err != nil {
			closeAll(parent)
			closeAll(child)
			return nil, fmt.Errorf("sdk session pipes: %w", err)
		}
		if len(child) == 0 {
			parent, child = append(parent, w), append(child, r)
		} else {
			parent, child = append(parent, r), append(child, w)
		}
	}
	cmd := exec.Command(cfg.Entry, "--sw-node-protocol")
	if cfg.Env == nil {
		cfg.Env = MinimalEnv()
	}
	cmd.Dir, cmd.Env = cfg.Dir, cfg.Env
	cmd.Stdout, cmd.Stderr = child[2], child[3]
	proc, err := startProcess(cmd, child[0], child[1])
	closeAll(child)
	if err != nil {
		closeAll(parent)
		return nil, fmt.Errorf("start sdk process: %w", err)
	}
	if cfg.Started != nil {
		cfg.Started(cmd.Process.Pid)
	}
	s := &Session{
		cfg:       cfg,
		cmd:       cmd,
		proc:      proc,
		sampler:   StartUsage(cmd.Process.Pid),
		requests:  parent[0],
		writer:    nodeproto.NewWriter(parent[0]),
		inflight:  map[string]call{},
		failed:    make(chan struct{}),
		reads:     parent[1:],
		frameEOF:  make(chan struct{}),
		watchDone: make(chan struct{}),
	}
	for range 3 {
		q := newSinkQueue()
		s.queues = append(s.queues, q)
		go q.run(cfg.Sink)
	}
	s.streams.Add(3)
	go s.readFrames(parent[1], s.queues[0])
	go s.readText(parent[2], LevelInfo, s.queues[1])
	go s.readText(parent[3], LevelWarn, s.queues[2])
	go s.watch(ctx)
	return s, nil
}

// Call sends req, with a fresh id, and waits for its reply. A failed reply is
// returned as a reply; the error reports a session that can no longer answer,
// as a [*nodeproto.Error] with a host code where the protocol names one.
// Cancelling ctx once the request is written stops only the wait: the request
// stays in flight. Cancelling it while the write is blocked fails the session
// with protocol_error, since the request pipe may hold half a request.
func (s *Session) Call(ctx context.Context, req nodeproto.Request) (nodeproto.Reply, error) {
	req.ID = "0"
	if err := req.Validate(); err != nil {
		return nodeproto.Reply{}, err
	}
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return nodeproto.Reply{}, s.err
	}
	for _, other := range s.inflight {
		if conflicts(req.Op, other.op) {
			s.mu.Unlock()
			return nodeproto.Reply{}, fmt.Errorf("%w: %s during %s", ErrBusy, req.Op, other.op)
		}
	}
	req.ID = s.tracker.Start(req.Op)
	c := call{op: req.Op, step: req.Step, reply: make(chan nodeproto.Reply, 1)}
	s.inflight[req.ID] = c
	s.mu.Unlock()

	// safety: the write runs apart from the caller so a process that stops
	// reading cannot hold a call, or the calls queued behind its write, past
	// their deadlines; tearing the session down unblocks it.
	written := make(chan error, 1)
	go func() { written <- s.writer.Send(req) }()
	select {
	case err := <-written:
		if err != nil {
			s.writeFailed(err)
		}
	case <-s.failed:
	case <-ctx.Done():
		s.fail(hostError(nodeproto.CodeProtocolError, "request %s (%s) not written before its deadline", req.ID, req.Op))
		return nodeproto.Reply{}, ctx.Err()
	}
	select {
	case r := <-c.reply:
		return r, nil
	case <-s.failed:
		// safety: a reply is delivered only while the session is live, so a
		// buffered one predates the failure.
		select {
		case r := <-c.reply:
			return r, nil
		default:
			return nodeproto.Reply{}, s.failure()
		}
	case <-ctx.Done():
		return nodeproto.Reply{}, ctx.Err()
	}
}

func (s *Session) writeFailed(err error) {
	// safety: a write usually breaks because the process exited, and the
	// watcher needs a moment to record that as sdk_exited.
	if s.failure() == nil && s.await(s.proc.LeaderExited()) {
		<-s.failed
	}
	s.fail(hostError(nodeproto.CodeProtocolError, "write sdk request: %v", err))
}

func conflicts(op, other nodeproto.Op) bool {
	alone := func(o nodeproto.Op) bool { return o == nodeproto.OpDescribe || o == nodeproto.OpPlan }
	body := func(o nodeproto.Op) bool { return o == nodeproto.OpRunNode || o == nodeproto.OpRunStep }
	return alone(op) || alone(other) ||
		(body(op) && body(other) && (op == nodeproto.OpRunNode || other == nodeproto.OpRunNode))
}

// Close closes the request pipe, which asks the process to exit, gives it the
// grace, then ends whatever is left of its session. It returns the session's
// usage and the failure that ended it, if any; later calls return the same.
func (s *Session) Close() (Usage, error) {
	s.closeOnce.Do(func() {
		s.fail(ErrClosed)
		_ = s.requests.Close()
		<-s.watchDone
		done := make(chan struct{})
		go func() {
			s.streams.Wait()
			close(done)
		}()
		// safety: a process that escaped the session can hold an output pipe
		// open forever, so the reads are cut off after the grace.
		if !s.await(done) {
			for _, f := range s.reads {
				_ = f.Close()
			}
			<-done
		}
		delivered := make(chan struct{})
		go func() {
			for _, q := range s.queues {
				q.close()
			}
			for _, q := range s.queues {
				<-q.done
			}
			close(delivered)
		}()
		// safety: a sink that never returns is abandoned after the grace so
		// the session still ends and its sampler stops; what it never got is
		// counted, so the log does not read as complete.
		abandoned := !s.await(delivered)
		for _, q := range s.queues {
			s.lost += q.lost(abandoned)
		}
		s.usage = s.sampler.Stop(s.cmd.ProcessState)
		err := s.failure()
		if errors.Is(err, ErrClosed) {
			s.mu.Lock()
			err = s.closingErr
			s.mu.Unlock()
		}
		s.closeErr = errors.Join(err, s.reapErr)
	})
	return s.usage, s.closeErr
}

// Lost is how many output lines and records the sink missed: those dropped
// while it was behind, and those still queued or in delivery when [Session.Close]
// gave up waiting for it. Read it only after Close returns.
func (s *Session) Lost() int { return s.lost }

func (s *Session) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var perr *nodeproto.Error
	if errors.Is(s.err, ErrClosed) && s.closingErr == nil && errors.As(err, &perr) && perr.Code == nodeproto.CodeProtocolError {
		s.closingErr = err
	}
	if s.err == nil {
		s.err = err
		close(s.failed)
	}
}

func (s *Session) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func hostError(code nodeproto.ErrorCode, format string, args ...any) error {
	return &nodeproto.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (s *Session) await(ch <-chan struct{}) bool {
	timer := time.NewTimer(s.cfg.Grace)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

func (s *Session) watch(ctx context.Context) {
	defer close(s.watchDone)
	leader := s.proc.LeaderExited()
	exited := false
	select {
	case <-ctx.Done():
		s.fail(fmt.Errorf("sdk session stopped: %w", context.Cause(ctx)))
	case <-leader:
		exited = true
		// safety: the last replies may still be in the pipe.
		s.await(s.frameEOF)
	case <-s.failed:
	case <-s.frameEOF:
	}
	if err := s.failure(); !exited && (err == nil || errors.Is(err, ErrClosed)) {
		// safety: an exiting process closes its frame pipe too, so the grace
		// tells an exit from a close, and gives a closed session time to end.
		if exited = s.await(leader); !exited {
			s.fail(hostError(nodeproto.CodeFramesClosed, "sdk closed its frame pipe"))
		}
	}
	reapCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*s.cfg.Grace+reapTimeout)
	defer cancel()
	if exited {
		s.reapErr = s.proc.Finish(reapCtx, s.cfg.Grace)
	} else {
		s.reapErr = s.proc.Terminate(reapCtx, s.cfg.Grace)
	}
	status := "status unknown"
	if s.cmd.ProcessState != nil {
		status = s.cmd.ProcessState.String()
	}
	s.fail(hostError(nodeproto.CodeSDKExited, "sdk exited (%s)", status))
}

func (s *Session) readFrames(f *os.File, q *sinkQueue) {
	defer s.streams.Done()
	defer f.Close()
	defer close(s.frameEOF)
	r := nodeproto.NewReader(f)
	for {
		frame, err := r.Next()
		switch {
		case errors.Is(err, nodeproto.ErrMalformed) || errors.Is(err, nodeproto.ErrLineTooLong):
			s.fail(hostError(nodeproto.CodeProtocolError, "%v", err))
			return
		case err != nil:
			return
		case frame.Kind == nodeproto.KindLog:
			rec := s.attribute(frame.Log)
			q.push(sinkItem{rec: &rec, size: len(rec.Msg) + len(rec.Attrs)})
			continue
		}
		if _, err := s.tracker.Resolve(frame.Reply); err != nil {
			s.fail(hostError(nodeproto.CodeProtocolError, "%v", err))
			return
		}
		s.mu.Lock()
		c, ok := s.inflight[frame.Reply.ID]
		delete(s.inflight, frame.Reply.ID)
		if ok && s.err == nil {
			c.reply <- frame.Reply
		}
		s.mu.Unlock()
	}
}

func (s *Session) attribute(rec nodeproto.LogRecord) nodeproto.LogRecord {
	if rec.Step == "" {
		return rec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.inflight {
		if c.op == nodeproto.OpRunStep && c.step == rec.Step {
			return rec
		}
	}
	rec.Step = ""
	return rec
}

func (s *Session) readText(f *os.File, level string, q *sinkQueue) {
	defer s.streams.Done()
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	var line []byte
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
			q.push(sinkItem{level: level, text: string(line[:end]), size: end})
			line = line[:0]
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

type sinkItem struct {
	level, text string
	rec         *nodeproto.LogRecord
	size        int
}

// safety: each stream's lines reach the sink through its own queue, so a pipe
// reader never waits on the sink.
type sinkQueue struct {
	mu      sync.Mutex
	items   []sinkItem
	bytes   int
	dropped int
	total   int
	closed  bool
	wake    chan struct{}
	done    chan struct{}
}

func newSinkQueue() *sinkQueue {
	return &sinkQueue{wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (q *sinkQueue) push(item sinkItem) {
	// safety: a fixed overhead per line bounds a flood of empty lines too.
	item.size += 64
	q.mu.Lock()
	if q.bytes > 0 && q.bytes+item.size > sinkQueueBytes {
		q.dropped++
		q.total++
	} else {
		q.items = append(q.items, item)
		q.bytes += item.size
	}
	q.mu.Unlock()
	q.signal()
}

// safety: an abandoned queue still runs, so its item in delivery counts as lost too.
func (q *sinkQueue) lost(abandoned bool) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.total
	if abandoned {
		select {
		case <-q.done:
		default:
			n += len(q.items) + 1
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
		if len(q.items) == 0 {
			dropped, closed := q.dropped, q.closed
			q.dropped = 0
			q.mu.Unlock()
			switch {
			case dropped > 0:
				// safety: a record, not text, so the notice never joins a stream's split line.
				sink.Record(nodeproto.LogRecord{
					TS: time.Now().UTC().Format(time.RFC3339Nano), Level: LevelWarn,
					Msg: fmt.Sprintf("sparkwing: %d log lines dropped while the log sink was behind", dropped),
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
		if item.rec != nil {
			sink.Record(*item.rec)
		} else {
			sink.Text(item.level, item.text)
		}
	}
}
