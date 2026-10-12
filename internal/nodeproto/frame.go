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
	// ErrLineTooLong reports a line longer than [MaxLineBytes]. A [Reader]
	// has already skipped the rest of the line, so the next read continues
	// with the following one.
	ErrLineTooLong = errors.New("nodeproto: line exceeds the maximum size")
	// ErrMalformed reports a request, or a line claiming to be a reply, that
	// does not have the protocol's shape.
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

// Reply answers the request whose id it carries. OK is true with Result, or
// false with Error.
type Reply struct {
	ID     string          `json:"reply"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Decode unmarshals a successful reply's result into v and runs v's Validate
// method when it has one. A failed reply returns its [*Error].
func (r Reply) Decode(v any) error {
	if !r.OK {
		if r.Error == nil {
			return fmt.Errorf("%w: reply %s failed without an error", ErrMalformed, r.ID)
		}
		return r.Error
	}
	if r.Result == nil {
		return fmt.Errorf("%w: reply %s has no result", ErrMalformed, r.ID)
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

// Kind says what a stdout line is.
type Kind int

// The kinds of stdout line.
const (
	KindText Kind = iota
	KindReply
	KindLog
)

// Frame is one classified stdout line; the field named by Kind is set.
type Frame struct {
	Kind  Kind
	Reply Reply
	Log   LogRecord
	Text  string
}

// Classify sorts one stdout line, without its newline, into a reply, a log
// record or text. A JSON object with a "reply" key is a reply and must have a
// reply's shape, or Classify fails with [ErrMalformed]. A JSON object with a
// string "ts" whose other known fields have the schema's types is a log
// record. Anything else is text, kept byte for byte. Keys match exactly, and
// unknown keys are ignored so a later SDK may add optional fields.
func Classify(line []byte) (Frame, error) {
	text := Frame{Kind: KindText, Text: string(line)}
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return text, nil
	}
	obj, ok := jsonObject(line)
	if !ok {
		return text, nil
	}
	if _, ok := obj["reply"]; ok {
		r, err := replyFrom(obj)
		if err != nil {
			return Frame{}, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		return Frame{Kind: KindReply, Reply: r}, nil
	}
	if rec, ok := logFrom(obj); ok {
		return Frame{Kind: KindLog, Log: rec}, nil
	}
	return text, nil
}

func jsonObject(line []byte) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	return obj, json.Unmarshal(line, &obj) == nil && obj != nil
}

func isNull(raw json.RawMessage) bool {
	return raw == nil || string(raw) == "null"
}

func stringField(obj map[string]json.RawMessage, key string, dst *string) bool {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return true
	}
	return json.Unmarshal(raw, dst) == nil
}

func replyFrom(obj map[string]json.RawMessage) (Reply, error) {
	var r Reply
	if json.Unmarshal(obj["reply"], &r.ID) != nil || r.ID == "" {
		return r, errors.New(`reply id is not a non-empty string`)
	}
	if isNull(obj["ok"]) || json.Unmarshal(obj["ok"], &r.OK) != nil {
		return r, fmt.Errorf("reply %s: ok is not a boolean", r.ID)
	}
	if raw := obj["result"]; !isNull(raw) {
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
	case !r.OK && (r.Error == nil || r.Error.Message == ""):
		return r, fmt.Errorf("reply %s: failed without an error message", r.ID)
	case !r.OK && r.Result != nil:
		return r, fmt.Errorf("reply %s: failed with a result", r.ID)
	}
	return r, nil
}

func logFrom(obj map[string]json.RawMessage) (LogRecord, bool) {
	var rec LogRecord
	if raw := obj["ts"]; isNull(raw) || json.Unmarshal(raw, &rec.TS) != nil {
		return rec, false
	}
	for key, dst := range map[string]*string{
		"level": &rec.Level, "node": &rec.Node, "step": &rec.Step,
		"event": &rec.Event, "msg": &rec.Msg,
	} {
		if !stringField(obj, key, dst) {
			return rec, false
		}
	}
	if raw := obj["attrs"]; !isNull(raw) {
		var attrs map[string]json.RawMessage
		if json.Unmarshal(raw, &attrs) != nil {
			return rec, false
		}
		rec.Attrs = raw
	}
	return rec, true
}

// Reader splits a stream into lines of at most [MaxLineBytes]. The host reads
// a process's stdout with [Reader.Next]; the SDK reads its stdin with
// [Reader.NextRequest].
type Reader struct {
	br  *bufio.Reader
	buf []byte
	max int
}

// NewReader reads lines from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r), max: MaxLineBytes}
}

// ReadLine returns the next line without its newline or a trailing carriage
// return. The slice is valid until the next read. A final line without a
// newline is still returned; io.EOF follows it.
func (r *Reader) ReadLine() ([]byte, error) {
	r.buf = r.buf[:0]
	tooLong := false
	for {
		chunk, err := r.br.ReadSlice('\n')
		if !tooLong {
			if len(r.buf)+len(chunk) > r.max+2 {
				// perf: drop what was buffered so an over-long line costs at
				// most the bound in memory, never its full length.
				tooLong, r.buf = true, r.buf[:0]
			} else {
				r.buf = append(r.buf, chunk...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return nil, err
		case tooLong:
			return nil, ErrLineTooLong
		case err != nil && len(r.buf) == 0:
			return nil, io.EOF
		}
		line := bytes.TrimSuffix(bytes.TrimSuffix(r.buf, []byte("\n")), []byte("\r"))
		if len(line) > r.max {
			return nil, ErrLineTooLong
		}
		return line, nil
	}
}

// Next reads and classifies the next stdout line. [ErrLineTooLong] and
// [ErrMalformed] leave the reader at the following line.
func (r *Reader) Next() (Frame, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Frame{}, err
	}
	return Classify(line)
}

// NextRequest reads and parses the next stdin line with [ParseRequest].
func (r *Reader) NextRequest() (Request, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Request{}, err
	}
	return ParseRequest(line)
}

// Writer writes one JSON value per line. It is safe for concurrent use, so a
// reply and the log records of a running node never interleave within a line.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	max int
}

// NewWriter writes lines to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, max: MaxLineBytes}
}

// Send writes v as one line. A value whose encoding exceeds [MaxLineBytes] is
// refused with [ErrLineTooLong] and nothing is written.
func (w *Writer) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > w.max {
		return ErrLineTooLong
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(append(b, '\n'))
	return err
}

// Tracker assigns request ids and matches replies to them. The zero value is
// ready to use and safe for concurrent use.
type Tracker struct {
	mu       sync.Mutex
	last     uint64
	pending  map[string]Op
	answered map[string]bool
}

// Start returns a fresh id for a request of op and records it as pending.
func (t *Tracker) Start(op Op) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending, t.answered = map[string]Op{}, map[string]bool{}
	}
	t.last++
	id := strconv.FormatUint(t.last, 10)
	t.pending[id] = op
	return id
}

// Resolve marks the request r answers as done and returns its op, so the
// caller knows which result type to decode. A reply to an id never started
// fails with [ErrUnknownReply] and a second reply with [ErrDuplicateReply].
func (t *Tracker) Resolve(r Reply) (Op, error) {
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
func (t *Tracker) Pending() map[string]Op {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Op, len(t.pending))
	for id, op := range t.pending {
		out[id] = op
	}
	return out
}
