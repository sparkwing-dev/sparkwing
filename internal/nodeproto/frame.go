package nodeproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

var (
	// ErrLineTooLong reports a line longer than [MaxLineBytes]. It ends a
	// [Reader]: the rest of the line is never read.
	ErrLineTooLong = errors.New("nodeproto: line exceeds the maximum size")
	// ErrMalformed reports a request or frame that does not have the
	// protocol's shape.
	ErrMalformed = errors.New("nodeproto: malformed line")
	// ErrUnknownReply reports a reply to an id the host never sent.
	ErrUnknownReply = errors.New("nodeproto: reply to an unknown request")
	// ErrDuplicateReply reports a second reply to the same request.
	ErrDuplicateReply = errors.New("nodeproto: duplicate reply")
)

// Error is the error object of a failed reply.
type Error struct {
	Code    ErrorCode `json:"code,omitempty"`
	Message string    `json:"message"`
}

// Error formats the code and message.
func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return string(e.Code) + ": " + e.Message
}

// ReplyOld answers the request whose id it carries. OK is true with Result, or
// false with Error.
type ReplyOld struct {
	ID     string          `json:"reply"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Decode unmarshals a successful reply's result into v and runs v's Validate
// method when it has one. A failed reply returns its [*Error].
func (r ReplyOld) Decode(v any) error {
	if !r.OK {
		if r.Error == nil {
			return fmt.Errorf("%w: reply %s failed without an error", ErrMalformed, r.ID)
		}
		return r.Error
	}
	if err := json.Unmarshal(r.Result, v); err != nil {
		return fmt.Errorf("%w: reply %s result: %w", ErrMalformed, r.ID, err)
	}
	if val, ok := v.(interface{ Validate() error }); ok {
		return val.Validate()
	}
	return nil
}

// LogRecord is one structured log line (docs/schemas/log-record.schema.json).
// Level and event values are left for the log pipeline to judge.
type LogRecord struct {
	TS    string          `json:"ts"`
	Level string          `json:"level,omitempty"`
	Node  string          `json:"node,omitempty"`
	Step  string          `json:"step,omitempty"`
	Event string          `json:"event,omitempty"`
	Msg   string          `json:"msg,omitempty"`
	Attrs json.RawMessage `json:"attrs,omitempty"`
}

// Kind says what a frame is.
type Kind int

// The kinds of frame.
const (
	KindReply Kind = iota + 1
	KindLog
)

// FrameOld is one line of the frame pipe; the field named by Kind is set.
type FrameOld struct {
	Kind  Kind
	Reply ReplyOld
	Log   LogRecord
}

// ParseFrameOld decodes one frame-pipe line, without its newline. A JSON object
// with a "reply" key is a reply; any other line must be a log record, a JSON
// object with a string "ts" whose other known fields have the schema's types.
// Everything else fails with [ErrMalformed], because only the SDK writes to
// the frame pipe. Keys match exactly, and unknown keys are ignored so a later
// SDK may add optional fields.
func ParseFrameOld(line []byte) (FrameOld, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(line, &obj); err != nil || obj == nil {
		return FrameOld{}, fmt.Errorf("%w: frame is not a JSON object", ErrMalformed)
	}
	if _, ok := obj["reply"]; ok {
		r, err := replyFrom(obj)
		if err != nil {
			return FrameOld{}, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		return FrameOld{Kind: KindReply, Reply: r}, nil
	}
	rec, err := logFrom(obj)
	if err != nil {
		return FrameOld{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return FrameOld{Kind: KindLog, Log: rec}, nil
}

func isNull(raw json.RawMessage) bool {
	return raw == nil || string(raw) == "null"
}

func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func stringField(obj map[string]json.RawMessage, key string, dst *string) bool {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return true
	}
	return json.Unmarshal(raw, dst) == nil
}

func replyFrom(obj map[string]json.RawMessage) (ReplyOld, error) {
	var r ReplyOld
	if json.Unmarshal(obj["reply"], &r.ID) != nil || r.ID == "" {
		return r, errors.New(`reply id is not a non-empty string`)
	}
	if isNull(obj["ok"]) || json.Unmarshal(obj["ok"], &r.OK) != nil {
		return r, fmt.Errorf("reply %s: ok is not a boolean", r.ID)
	}
	if raw := obj["result"]; !isNull(raw) {
		if !isObject(raw) {
			return r, fmt.Errorf("reply %s: result is not an object", r.ID)
		}
		r.Result = raw
	}
	if raw := obj["error"]; !isNull(raw) {
		var eobj map[string]json.RawMessage
		r.Error = &Error{}
		var code string
		if json.Unmarshal(raw, &eobj) != nil || !stringField(eobj, "code", &code) ||
			!stringField(eobj, "message", &r.Error.Message) {
			return r, fmt.Errorf("reply %s: error is not {code?, message}", r.ID)
		}
		r.Error.Code = ErrorCode(code)
	}
	switch {
	case r.OK && r.Error != nil:
		return r, fmt.Errorf("reply %s: ok with an error", r.ID)
	case r.OK && r.Result == nil:
		return r, fmt.Errorf("reply %s: ok without a result", r.ID)
	case !r.OK && (r.Error == nil || r.Error.Message == ""):
		return r, fmt.Errorf("reply %s: failed without an error message", r.ID)
	case !r.OK && r.Result != nil:
		return r, fmt.Errorf("reply %s: failed with a result", r.ID)
	}
	return r, nil
}

func logFrom(obj map[string]json.RawMessage) (LogRecord, error) {
	var rec LogRecord
	if raw := obj["ts"]; isNull(raw) || json.Unmarshal(raw, &rec.TS) != nil {
		return rec, errors.New("log record has no string ts")
	}
	for key, dst := range map[string]*string{
		"level": &rec.Level, "node": &rec.Node, "step": &rec.Step,
		"event": &rec.Event, "msg": &rec.Msg,
	} {
		if !stringField(obj, key, dst) {
			return rec, fmt.Errorf("log record %s is not a string", key)
		}
	}
	if raw := obj["attrs"]; !isNull(raw) {
		if !isObject(raw) {
			return rec, errors.New("log record attrs is not an object")
		}
		rec.Attrs = raw
	}
	return rec, nil
}

// Reader splits a pipe into lines. The host reads the frame pipe with
// [Reader.Next]; the SDK reads the request pipe with [Reader.NextRequest].
type Reader struct {
	br  *bufio.Reader
	buf []byte
	max int
	err error
}

// NewReader reads lines from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r), max: MaxLineBytes}
}

// ReadLine returns the next line without its newline or a trailing carriage
// return. The slice is valid until the next read. A final line without a
// newline is still returned; io.EOF follows it. A line of more than
// [MaxLineBytes], newline included, fails with [ErrLineTooLong] as soon as
// the bound is crossed, without waiting for the newline, and every later
// read fails the same way.
func (r *Reader) ReadLine() ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.buf = r.buf[:0]
	for {
		chunk, err := r.br.ReadSlice('\n')
		r.buf = append(r.buf, chunk...)
		if errors.Is(err, io.EOF) && len(r.buf) == 0 {
			return nil, io.EOF
		}
		size := len(r.buf)
		if !bytes.HasSuffix(r.buf, []byte("\n")) {
			size++
		}
		if size > r.max {
			r.err = ErrLineTooLong
			return nil, r.err
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return nil, err
		}
		return bytes.TrimSuffix(bytes.TrimSuffix(r.buf, []byte("\n")), []byte("\r")), nil
	}
}

// Next reads and parses the next frame-pipe line with [ParseFrameOld].
// [ErrMalformed] leaves the reader at the following line.
func (r *Reader) Next() (FrameOld, error) {
	line, err := r.ReadLine()
	if err != nil {
		return FrameOld{}, err
	}
	return ParseFrameOld(line)
}

// NextRequest reads and parses the next request-pipe line with [ParseRequestOld].
func (r *Reader) NextRequest() (RequestOld, error) {
	line, err := r.ReadLine()
	if err != nil {
		return RequestOld{}, err
	}
	return ParseRequestOld(line)
}

// Writer writes one JSON value per line. It is safe for concurrent use, so the
// replies of parallel run_step and eval requests and the log records written
// meanwhile never interleave within a line.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	max int
}

// NewWriter writes lines to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, max: MaxLineBytes}
}

// Send writes v as one line. A value whose line, newline included, exceeds
// [MaxLineBytes] is refused with [ErrLineTooLong] and nothing is written.
func (w *Writer) Send(v any) error {
	// perf: encode under the lock, so senders queued behind a slow reader
	// hold their values but never a second, encoded copy of each.
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > w.max {
		return ErrLineTooLong
	}
	_, err = w.w.Write(append(b, '\n'))
	return err
}

// Tracker assigns request ids and matches replies to them. Any number of
// requests may be pending, since run_step and eval requests run in parallel;
// which ops may overlap is the session's rule, not the tracker's. The zero
// value is ready to use and safe for concurrent use.
type Tracker struct {
	mu       sync.Mutex
	last     uint64
	pending  map[string]OpOld
	answered map[string]bool
}

// Start returns a fresh id for a request of op and records it as pending.
func (t *Tracker) Start(op OpOld) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending, t.answered = map[string]OpOld{}, map[string]bool{}
	}
	t.last++
	id := strconv.FormatUint(t.last, 10)
	t.pending[id] = op
	return id
}

// Resolve marks the request r answers as done and returns its op, so the
// caller knows which result type to decode. A reply to an id never started
// fails with [ErrUnknownReply] and a second reply with [ErrDuplicateReply].
func (t *Tracker) Resolve(r ReplyOld) (OpOld, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	op, ok := t.pending[r.ID]
	if !ok {
		if t.answered[r.ID] {
			return "", fmt.Errorf("%w: %s", ErrDuplicateReply, r.ID)
		}
		return "", fmt.Errorf("%w: %s", ErrUnknownReply, r.ID)
	}
	delete(t.pending, r.ID)
	t.answered[r.ID] = true
	return op, nil
}

// Pending returns the requests still waiting for a reply, by id, so a host
// whose process exited can fail each one.
func (t *Tracker) Pending() map[string]OpOld {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]OpOld, len(t.pending))
	for id, op := range t.pending {
		out[id] = op
	}
	return out
}
