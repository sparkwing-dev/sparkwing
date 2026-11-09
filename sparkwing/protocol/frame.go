package nodeproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

var (
	// errLineTooLong reports a line longer than [MaxLineBytes]. It ends a
	// [Reader]: the rest of the line is never read.
	errLineTooLong = errors.New("nodeproto: line exceeds the maximum size")
	// ErrMalformed reports a request or frame that does not have the
	// protocol's shape.
	ErrMalformed = errors.New("nodeproto: malformed line")
)

func isObject(raw json.RawMessage) bool { b := bytes.TrimSpace(raw); return len(b) > 0 && b[0] == '{' }

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
// [MaxLineBytes], newline included, fails with [errLineTooLong] as soon as
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
			r.err = errLineTooLong
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

// Next reads and parses the next frame-pipe line with [ParseFrame].
// [ErrMalformed] leaves the reader at the following line.
func (r *Reader) Next() (Frame, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Frame{}, err
	}
	return ParseFrame(line)
}

// NextRequest reads and parses the next request-pipe line with [ParseRequest].
func (r *Reader) NextRequest() (Request, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Request{}, err
	}
	return ParseRequest(line)
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
// [MaxLineBytes] is refused with [errLineTooLong] and nothing is written.
func (w *Writer) Send(v any) error {
	// safety: encode under the lock, so senders queued behind a slow reader
	// hold their values but never a second, encoded copy of each.
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > w.max {
		return errLineTooLong
	}
	n, err := w.w.Write(append(b, '\n'))
	if err == nil && n != len(b)+1 {
		return io.ErrShortWrite
	}
	return err
}
