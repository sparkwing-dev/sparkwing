package secrets

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// MaskRecord masks a decoded log record's message and attribute values with
// every value the child registered before it wrote the record.
func (v *ChildValues) MaskRecord(rec sparkwing.LogRecord) sparkwing.LogRecord {
	m := v.synced()
	rec.Msg = m.Mask(rec.Msg)
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

// perf: a line longer than this is written in pieces rather than held whole.
const maxMaskedLine = 1 << 20

// LineWriter masks a child's output a line at a time.
type LineWriter struct {
	v   *ChildValues
	dst io.Writer
	mu  sync.Mutex
	buf []byte
	err error
}

// Write never fails, so a destination that refuses output never blocks the
// child on a broken pipe.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	start := 0
	for {
		i := bytes.IndexByte(w.buf[start:], '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[start : start+i+1])
		start += i + 1
	}
	w.buf = append(w.buf[:0], w.buf[start:]...)
	if len(w.buf) > maxMaskedLine {
		cut := safeCut(string(w.buf), w.v.synced().Values())
		w.emit(w.buf[:cut])
		w.buf = append(w.buf[:0], w.buf[cut:]...)
	}
	return len(p), nil
}

// safety: a value can only cross the split if the bytes before it end with a
// proper prefix of that value, so keeping the longest such suffix keeps every
// crossing value whole, and that suffix is shorter than the longest value.
func safeCut(s string, patterns []string) int {
	keep := 0
	for _, p := range patterns {
		for k := min(len(p)-1, len(s)); k > keep; k-- {
			if strings.HasSuffix(s, p[:k]) {
				keep = k
				break
			}
		}
	}
	return len(s) - keep
}

func (w *LineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *LineWriter) emit(line []byte) {
	if w.err == nil {
		_, w.err = io.WriteString(w.dst, w.v.Mask(string(line)))
	}
}
