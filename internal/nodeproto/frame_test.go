package nodeproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestParseFrame(t *testing.T) {
	cases := []struct {
		line string
		kind Kind
		bad  bool
	}{
		{`{"reply":"1","ok":true,"result":{"outcome":"success"}}`, KindReply, false},
		{`{"reply":"2","ok":false,"error":{"code":"unknown_node","message":"no node x"}}`, KindReply, false},
		{`{"ts":"2026-01-02T03:04:05Z","level":"info","msg":"hi","attrs":{"k":1}}`, KindLog, false},
		{`{"ts":"2026-01-02T03:04:05Z","later_field":true}`, KindLog, false},
		{`plain output`, 0, true},
		{``, 0, true},
		{`null`, 0, true},
		{`{"ts":5}`, 0, true},
		{`{"ts":"x","msg":7}`, 0, true},
		{`{"ts":"x","attrs":[1]}`, 0, true},
		{`{"TS":"x"}`, 0, true},
		{`{"ReplyOld":"1","ok":true}`, 0, true},
		{`{"id":"1","op":"describe"}`, 0, true},
		{`{"reply":"1","ok":true,"result":{}} trailing`, 0, true},
		{`{"reply":"","ok":true}`, 0, true},
		{`{"reply":"1"}`, 0, true},
		{`{"reply":"1","ok":null,"error":{"message":"x"}}`, 0, true},
		{`{"reply":"1","ok":true,"error":{"message":"x"}}`, 0, true},
		{`{"reply":"1","ok":false}`, 0, true},
		{`{"reply":"1","ok":false,"error":{"code":"plan_drift"}}`, 0, true},
		{`{"reply":"1","ok":false,"error":"boom"}`, 0, true},
		{`{"reply":"1","ok":false,"result":{},"error":{"message":"x"}}`, 0, true},
		{`{"reply":"1","ok":true}`, 0, true},
		{`{"reply":"1","ok":true,"result":null}`, 0, true},
		{`{"reply":"1","ok":true,"result":[1]}`, 0, true},
	}
	for _, c := range cases {
		f, err := ParseFrameOld([]byte(c.line))
		if c.bad {
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("%s: err = %v, want ErrMalformed", c.line, err)
			}
			continue
		}
		if err != nil || f.Kind != c.kind {
			t.Errorf("%s: kind %d err %v, want kind %d", c.line, f.Kind, err, c.kind)
		}
	}
}

func TestReplyDecode(t *testing.T) {
	f, err := ParseFrameOld([]byte(`{"reply":"1","ok":true,"result":{"outcome":"failed","error":"exit 1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var res RunResultOld
	if err := f.Reply.Decode(&res); err != nil || res.Outcome != OutcomeFailed || res.Error != "exit 1" {
		t.Fatalf("decode = %+v, %v", res, err)
	}
	f, _ = ParseFrameOld([]byte(`{"reply":"1","ok":true,"result":{"outcome":"maybe"}}`))
	if err := f.Reply.Decode(&res); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown outcome err = %v", err)
	}
	f, _ = ParseFrameOld([]byte(`{"reply":"1","ok":false,"error":{"code":"plan_drift","message":"hash differs"}}`))
	var perr *Error
	if err := f.Reply.Decode(&res); !errors.As(err, &perr) || perr.Code != CodePlanDrift {
		t.Fatalf("failed reply err = %v", err)
	}
	f, _ = ParseFrameOld([]byte(`{"reply":"1","ok":true,"result":{"outcome":"skipped"}}`))
	if err := f.Reply.Decode(&res); !errors.Is(err, ErrMalformed) {
		t.Fatalf("skipped is not a body outcome, err = %v", err)
	}
	f, _ = ParseFrameOld([]byte(`{"reply":"1","ok":true,"result":{"value":null,"error":"503 from /ready"}}`))
	var ev EvalResultOld
	if err := f.Reply.Decode(&ev); err != nil || ev.Error == nil || *ev.Error != "503 from /ready" || string(ev.Value) != "null" {
		t.Fatalf("eval decode = %+v, %v", ev, err)
	}
}

func TestEvalEmptyErrorStillFails(t *testing.T) {
	f, err := ParseFrameOld([]byte(`{"reply":"1","ok":true,"result":{"value":null,"error":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	var ev EvalResultOld
	if err := f.Reply.Decode(&ev); err != nil || ev.Error == nil {
		t.Fatalf("empty error lost: %+v, %v", ev, err)
	}
	empty := ""
	enc, err := json.Marshal(EvalResultOld{Error: &empty})
	if err != nil || string(enc) != `{"value":null,"error":""}` {
		t.Fatalf("encode = %s, %v", enc, err)
	}
}

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
	if req, err := r.NextRequest(); err != nil || req.Op != OpDescribeOld {
		t.Fatalf("describe: %+v %v", req, err)
	}
	req, err := r.NextRequest()
	var perr *Error
	if !errors.As(err, &perr) || perr.Code != CodeUnknownOp || req.ID != "2" {
		t.Fatalf("unknown op: %+v %v", req, err)
	}
	if _, err := r.NextRequest(); !errors.Is(err, ErrMalformed) {
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
	if err := w.Send(LogRecord{TS: "t", Msg: strings.Repeat("m", 64)}); !errors.Is(err, ErrLineTooLong) || buf.Len() != 0 {
		t.Fatalf("oversize: err %v, wrote %d bytes", err, buf.Len())
	}
	w.max = MaxLineBytes
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Send(ReplyOld{ID: strings.Repeat("i", i+1), OK: true, Result: json.RawMessage(`{}`)})
		}()
	}
	wg.Wait()
	r := NewReader(&buf)
	for range 50 {
		if f, err := r.Next(); err != nil || f.Kind != KindReply {
			t.Fatalf("interleaved line: %+v %v", f, err)
		}
	}
}

func TestTracker(t *testing.T) {
	var tr Tracker
	a, b := tr.Start(OpPlanOld), tr.Start(OpEvalOld)
	if a == b {
		t.Fatalf("ids repeat: %s", a)
	}
	if op, err := tr.Resolve(ReplyOld{ID: b}); err != nil || op != OpEvalOld {
		t.Fatalf("resolve b: %s %v", op, err)
	}
	if _, err := tr.Resolve(ReplyOld{ID: b}); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("second reply: %v", err)
	}
	if _, err := tr.Resolve(ReplyOld{ID: "99"}); !errors.Is(err, ErrUnknownReply) {
		t.Fatalf("unknown reply: %v", err)
	}
	if p := tr.Pending(); len(p) != 1 || p[a] != OpPlanOld {
		t.Fatalf("pending %v", p)
	}
}

func TestTrackerParallelSteps(t *testing.T) {
	var tr Tracker
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = tr.Start(OpRunStepOld)
	}
	var wg sync.WaitGroup
	for i := len(ids) - 1; i >= 0; i-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if op, err := tr.Resolve(ReplyOld{ID: ids[i]}); err != nil || op != OpRunStepOld {
				t.Errorf("resolve %s: %s %v", ids[i], op, err)
			}
		}()
	}
	wg.Wait()
	if p := tr.Pending(); len(p) != 0 {
		t.Fatalf("pending after all replies: %v", p)
	}
}

func FuzzParseFrame(f *testing.F) {
	for _, s := range []string{
		`{"reply":"1","ok":true,"result":{"outcome":"success","output":{"a":[1,2]}}}`,
		`{"reply":"1","ok":false,"error":{"code":"unknown_op","message":"x"}}`,
		`{"ts":"2026-01-02T03:04:05.123Z","level":"error","node":"lint","event":"node_annotation","msg":"m","attrs":{"message":"m","line":42}}`,
		`{"ts":"t","msg":null,"attrs":null}`,
		`{"reply":null}`, `{"reply":"\u0000","ok":true,"result":null}`, `text {`, `{`, `[]`, `null`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		fr, err := ParseFrameOld(line)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("error outside ErrMalformed: %v", err)
			}
			return
		}
		var v any
		switch fr.Kind {
		case KindReply:
			v = fr.Reply
		case KindLog:
			v = fr.Log
		default:
			t.Fatalf("frame parsed without a kind: %+v", fr)
		}
		enc, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("re-encode %+v: %v", v, err)
		}
		again, err := ParseFrameOld(enc)
		if err != nil || again.Kind != fr.Kind {
			t.Fatalf("re-encoded %s parsed as kind %d, %v", enc, again.Kind, err)
		}
		v = again.Log
		if again.Kind == KindReply {
			v = again.Reply
		}
		enc2, _ := json.Marshal(v)
		if !bytes.Equal(enc, enc2) {
			t.Fatalf("round trip drift:\n%s\n%s", enc, enc2)
		}
	})
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
