package nodeproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"io"
	"strings"
	"sync"
	"testing"
)

func readAll(t *testing.T, r *Reader) ([]string, error) {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadLine()
		if err != nil {
			return lines, err
		}
		lines = append(lines, string(line))
	}
}

func TestReaderLineBound(t *testing.T) {
	in := "a\r\n" + strings.Repeat("y", 63) + "\n" + strings.Repeat("y", 64) + "\nnever read\n"
	r := NewReader(strings.NewReader(in))
	r.max = 64
	lines, err := readAll(t, r)
	if want := []string{"a", strings.Repeat("y", 63)}; strings.Join(lines, "|") != strings.Join(want, "|") || !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("lines %q, err %v", lines, err)
	}
	if _, err := r.ReadLine(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("read after a long line: %v", err)
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReaderFailsLongLineWithoutNewline(t *testing.T) {
	r := NewReader(endless{})
	r.max = 10000
	if _, err := r.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
	if len(r.buf) > r.max+4096 {
		t.Fatalf("buffered %d bytes past a %d byte bound", len(r.buf), r.max)
	}
}

func TestReaderNextRequest(t *testing.T) {
	r := NewReader(strings.NewReader(`{"id":"1","op":"describe"}` + "\n" + `{"id":"2","op":"teleport"}` + "\nnot json\n"))
	if req, err := r.NextRequest(); err != nil || req.Op != OpDescribe {
		t.Fatalf("describe: %+v %v", req, err)
	}
	req, err := r.NextRequest()
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.UnknownOp || req.ID != "2" {
		t.Fatalf("unknown op: %+v %v", req, err)
	}
	if _, err := r.NextRequest(); err == nil {
		t.Fatalf("not json: %v", err)
	}
	if _, err := r.NextRequest(); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
}

func TestWriterRefusesOversizeAndKeepsLinesWhole(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.max = 32
	if err := w.Send(FrameLog{TS: "t", Message: strings.Repeat("m", 64)}); !errors.Is(err, ErrLineTooLong) || buf.Len() != 0 {
		t.Fatalf("oversize: err %v, wrote %d bytes", err, buf.Len())
	}
	w.max = MaxLineBytes
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Send(Reply{ID: strings.Repeat("i", i+1), OK: true, Result: json.RawMessage(`{}`)})
		}()
	}
	wg.Wait()
	r := NewReader(&buf)
	for range 50 {
		if f, err := r.Next(); err != nil || f.Reply == nil {
			t.Fatalf("interleaved line: %+v %v", f, err)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestWriterReportsIncompleteFrame(t *testing.T) {
	if err := NewWriter(shortWriter{}).Send(FrameLog{TS: "now", Message: "x"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial protocol frame reported success: %v", err)
	}
}

func TestTracker(t *testing.T) {
	var tr Tracker
	a, b := tr.Start(OpPlan), tr.Start(OpEval)
	if a == b {
		t.Fatalf("ids repeat: %s", a)
	}
	if op, err := tr.Resolve(Reply{ID: b}); err != nil || op != OpEval {
		t.Fatalf("resolve b: %s %v", op, err)
	}
	if _, err := tr.Resolve(Reply{ID: b}); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("second reply: %v", err)
	}
	if _, err := tr.Resolve(Reply{ID: "99"}); !errors.Is(err, ErrUnknownReply) {
		t.Fatalf("unknown reply: %v", err)
	}
	if p := tr.Pending(); len(p) != 1 || p[a] != OpPlan {
		t.Fatalf("pending %v", p)
	}
}

func TestTrackerParallelSteps(t *testing.T) {
	var tr Tracker
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = tr.Start(OpRunStep)
	}
	var wg sync.WaitGroup
	for i := len(ids) - 1; i >= 0; i-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if op, err := tr.Resolve(Reply{ID: ids[i]}); err != nil || op != OpRunStep {
				t.Errorf("resolve %s: %s %v", ids[i], op, err)
			}
		}()
	}
	wg.Wait()
	if p := tr.Pending(); len(p) != 0 {
		t.Fatalf("pending after all replies: %v", p)
	}
}

func FuzzReader(f *testing.F) {
	f.Add([]byte("a\nbb\r\n\n{\"ts\":\"t\"}\nccccccccccccccccccccccc\ntail"), uint8(4))
	f.Add([]byte(strings.Repeat("z", 300)), uint8(0))
	f.Fuzz(func(t *testing.T, in []byte, max uint8) {
		r := NewReader(bytes.NewReader(in))
		r.max = int(max)
		got, err := readAll(t, r)
		for _, line := range got {
			if len(line)+1 > r.max {
				t.Fatalf("line of %d bytes passed max %d", len(line), r.max)
			}
		}
		var want []string
		wantErr := io.EOF
		parts := strings.Split(string(in), "\n")
		if parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		for _, p := range parts {
			if len(p)+1 > r.max {
				wantErr = ErrLineTooLong
				break
			}
			want = append(want, strings.TrimSuffix(p, "\r"))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != len(want) || !errors.Is(err, wantErr) {
			t.Fatalf("got %q (%v), want %q (%v)", got, err, want, wantErr)
		}
	})
}
