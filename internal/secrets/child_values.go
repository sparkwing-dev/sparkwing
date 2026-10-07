package secrets

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// MaskValuesFDEnv names the inherited file descriptor a pipeline process
// writes every value it registers to, one base64 line each, so the process
// that started it masks the pipeline's raw stdout and stderr too: output
// printed without the SDK, by a child inheriting stdio, or by the runtime for
// an unrecovered panic.
const MaskValuesFDEnv = "SPARKWING_MASK_VALUES_FD"

var (
	shareMu sync.Mutex
	shareTo io.Writer
)

// ShareRegisteredFromEnv makes every later [Masker.Register] in this process
// also write the value to the descriptor [MaskValuesFDEnv] names. Without the
// variable, registering behaves as before.
func ShareRegisteredFromEnv() {
	raw, ok := os.LookupEnv(MaskValuesFDEnv)
	if !ok {
		return
	}
	// safety: a process this one starts would read an unrelated descriptor
	// of its own under the same number.
	if err := os.Unsetenv(MaskValuesFDEnv); err != nil {
		return
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return
	}
	f := inheritedValuesFile(fd)
	if f == nil {
		return
	}
	shareMu.Lock()
	shareTo = f
	shareMu.Unlock()
}

func shareRegistered(value string) {
	shareMu.Lock()
	defer shareMu.Unlock()
	if shareTo == nil {
		return
	}
	// safety: a launcher that has gone cannot be told, and the pipeline's own
	// masking still holds, so a failed write ends sharing, not the registration.
	if _, err := io.WriteString(shareTo, base64.StdEncoding.EncodeToString([]byte(value))+"\n"); err != nil {
		shareTo = nil
	}
}

// ChildValues masks a child process's raw output with every value the child
// registers through [MaskValuesFDEnv]. Create it before the child starts and
// Close it after the child has been waited for.
type ChildValues struct {
	masker  *Masker
	r, w    *os.File
	mu      sync.Mutex
	cond    *sync.Cond
	done    bool
	frame   []byte
	writers []*LineWriter
}

// ShareWithChild hands cmd the write end of a values pipe and returns the
// reader that masks cmd's output with what arrives on it.
func ShareWithChild(cmd *exec.Cmd) (*ChildValues, error) {
	v := &ChildValues{masker: NewMasker()}
	v.cond = sync.NewCond(&v.mu)
	if !valuesChannelSupported {
		v.done = true
		return v, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("mask values pipe: %w", err)
	}
	v.r, v.w = r, w
	cmd.ExtraFiles = append(cmd.ExtraFiles, w)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, MaskValuesFDEnv+"="+strconv.Itoa(2+len(cmd.ExtraFiles)))
	go v.pump()
	return v, nil
}

// Mask masks s with every value the child registered before it wrote s.
func (v *ChildValues) Mask(s string) string {
	return v.synced().Mask(s)
}

// MaskJSON masks every string value of a JSON document, keeping its keys,
// structure and numbers, with every value the child registered before it
// wrote the document.
func (v *ChildValues) MaskJSON(doc []byte) []byte {
	return v.synced().MaskJSON(doc)
}

// MaskRecord masks every string field of a decoded log record and its
// attribute values with every value the child registered before it wrote
// the record.
func (v *ChildValues) MaskRecord(rec sparkwing.LogRecord) sparkwing.LogRecord {
	m := v.synced()
	rec.Level, rec.JobID, rec.Step = m.Mask(rec.Level), m.Mask(rec.JobID), m.Mask(rec.Step)
	rec.Event, rec.Msg = m.Mask(rec.Event), m.Mask(rec.Msg)
	rec.Attrs = m.MaskAttrs(rec.Attrs)
	return rec
}

func (v *ChildValues) synced() *Masker {
	v.mu.Lock()
	// safety: the child writes a value here before Register returns, so any
	// bytes still in the pipe may name a value the output already holds.
	for !v.done && v.pending() > 0 {
		v.cond.Wait()
	}
	v.mu.Unlock()
	return v.masker
}

func (v *ChildValues) take(b []byte) {
	v.frame = append(v.frame, b...)
	start := 0
	for {
		i := bytes.IndexByte(v.frame[start:], '\n')
		if i < 0 {
			break
		}
		if value, err := base64.StdEncoding.DecodeString(string(v.frame[start : start+i])); err == nil {
			v.masker.Register(string(value))
		}
		start += i + 1
	}
	v.frame = append(v.frame[:0], v.frame[start:]...)
}

func (v *ChildValues) finish() {
	v.mu.Lock()
	v.done = true
	v.cond.Broadcast()
	v.mu.Unlock()
}

// Writer returns a writer that masks each complete line before writing it to
// dst. Close writes a trailing unterminated line.
func (v *ChildValues) Writer(dst io.Writer) *LineWriter {
	w := &LineWriter{v: v, dst: dst}
	v.mu.Lock()
	v.writers = append(v.writers, w)
	v.mu.Unlock()
	return w
}

// Close flushes every writer and releases the pipe. Call it after the child
// has been waited for, so no more output arrives.
func (v *ChildValues) Close() {
	v.mu.Lock()
	writers := v.writers
	v.mu.Unlock()
	for _, w := range writers {
		w.flush()
	}
	if v.r != nil {
		_ = v.w.Close()
		_ = v.r.Close()
	}
}

// perf: an unterminated line longer than this is dropped rather than held whole.
const maxMaskedLine = 1 << 20

// LineWriter masks a child's output a line at a time.
type LineWriter struct {
	v       *ChildValues
	dst     io.Writer
	mu      sync.Mutex
	buf     []byte
	dropped int
	err     error
}

// Write never fails, so a destination that refuses output never blocks the
// child on a broken pipe.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.take(p)
			break
		}
		w.take(p[:i])
		w.endLine("\n")
		p = p[i+1:]
	}
	return n, nil
}

// safety: any part of an overlong line written on its own could end inside a
// value whose rest the masker never sees with it, so the whole line goes.
func (w *LineWriter) take(p []byte) {
	if w.dropped > 0 {
		w.dropped += len(p)
		return
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > maxMaskedLine {
		w.buf, w.dropped = w.buf[:0], len(w.buf)
	}
}

func (w *LineWriter) endLine(end string) {
	if w.dropped > 0 {
		w.write(fmt.Sprintf("[line over 1 MiB dropped: %d bytes]%s", w.dropped, end))
	} else if len(w.buf) > 0 || end != "" {
		w.write(w.v.Mask(string(w.buf)) + end)
	}
	w.buf, w.dropped = w.buf[:0], 0
}

func (w *LineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.endLine("")
}

func (w *LineWriter) write(s string) {
	if w.err == nil {
		_, w.err = io.WriteString(w.dst, s)
	}
}
