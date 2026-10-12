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
		{`{"Reply":"1","ok":true}`, 0, true},
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
		f, err := ParseFrame([]byte(c.line))
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
	f, err := ParseFrame([]byte(`{"reply":"1","ok":true,"result":{"outcome":"failed","error":"exit 1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	if err := f.Reply.Decode(&res); err != nil || res.Outcome != OutcomeFailed || res.Error != "exit 1" {
		t.Fatalf("decode = %+v, %v", res, err)
	}
	f, _ = ParseFrame([]byte(`{"reply":"1","ok":true,"result":{"outcome":"maybe"}}`))
	if err := f.Reply.Decode(&res); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown outcome err = %v", err)
	}
	f, _ = ParseFrame([]byte(`{"reply":"1","ok":false,"error":{"code":"plan_drift","message":"hash differs"}}`))
	var perr *Error
	if err := f.Reply.Decode(&res); !errors.As(err, &perr) || perr.Code != CodePlanDrift {
		t.Fatalf("failed reply err = %v", err)
	}
	f, _ = ParseFrame([]byte(`{"reply":"1","ok":true,"result":{"outcome":"skipped"}}`))
	if err := f.Reply.Decode(&res); !errors.Is(err, ErrMalformed) {
		t.Fatalf("skipped is not a body outcome, err = %v", err)
	}
	f, _ = ParseFrame([]byte(`{"reply":"1","ok":true,"result":{"value":null,"error":"503 from /ready"}}`))
	var ev EvalResult
	if err := f.Reply.Decode(&ev); err != nil || ev.Error != "503 from /ready" || string(ev.Value) != "null" {
		t.Fatalf("eval decode = %+v, %v", ev, err)
	}
}

func readAll(t *testing.T, r *Reader) (lines []string, tooLong int) {
	t.Helper()
	for {
		line, err := r.ReadLine()
		switch {
		case errors.Is(err, io.EOF):
			return lines, tooLong
		case errors.Is(err, ErrLineTooLong):
			tooLong++
		case err != nil:
			t.Fatal(err)
		default:
			lines = append(lines, string(line))
		}
	}
}

func TestReaderLineBound(t *testing.T) {
	long := strings.Repeat("x", 10000)
	in := "a\r\n" + long + "\nb\n" + strings.Repeat("y", 63) + "\n" + strings.Repeat("y", 64) + "\nlast"
	r := NewReader(strings.NewReader(in))
	r.max = 64
	lines, tooLong := readAll(t, r)
	if want := []string{"a", "b", strings.Repeat("y", 63), "last"}; strings.Join(lines, "|") != strings.Join(want, "|") || tooLong != 2 {
		t.Fatalf("lines %q, too long %d", lines, tooLong)
	}
	r = NewReader(strings.NewReader("ok\n" + long))
	r.max = 64
	if lines, tooLong = readAll(t, r); len(lines) != 1 || tooLong != 1 {
		t.Fatalf("unterminated long tail: lines %q, too long %d", lines, tooLong)
	}
}

func TestReaderNextRequest(t *testing.T) {
	r := NewReader(strings.NewReader(`{"id":"1","op":"describe"}` + "\n" + `{"id":"2","op":"teleport"}` + "\nnot json\n"))
	if req, err := r.NextRequest(); err != nil || req.Op != OpDescribe {
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
			_ = w.Send(Reply{ID: strings.Repeat("i", i+1), OK: true, Result: json.RawMessage(`{}`)})
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
		fr, err := ParseFrame(line)
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
		again, err := ParseFrame(enc)
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
		var got []string
		tooLong := 0
		for {
			line, err := r.ReadLine()
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, ErrLineTooLong) {
				tooLong++
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(line)+1 > r.max {
				t.Fatalf("line of %d bytes passed max %d", len(line), r.max)
			}
			got = append(got, string(line))
		}
		var want []string
		wantLong := 0
		parts := strings.Split(string(in), "\n")
		if parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		for _, p := range parts {
			if len(p)+1 > r.max {
				wantLong++
				continue
			}
			want = append(want, strings.TrimSuffix(p, "\r"))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != len(want) || tooLong != wantLong {
			t.Fatalf("got %q (%d too long), want %q (%d too long)", got, tooLong, want, wantLong)
		}
	})
}
