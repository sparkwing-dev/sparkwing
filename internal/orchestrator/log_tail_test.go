package orchestrator

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTailMatchesExistingFiltersAndRendering(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, data := range []string{"", "\n", "a", "a\nb", "a\nb\n", "a\n\n\n", "a\r\nb\r\n", "α\n日本語\nlast", strings.Repeat("long", 20000) + "\nlast\n", "{\"msg\":\"first\"}\n\n{\"msg\":\"last\"}\n", "{\"msg\":\"json first\"}\nnot json\n"} {
		for _, tail := range []int{1, 2, 40} {
			for _, format := range []string{"json", "plain", "pretty"} {
				path := filepath.Join(t.TempDir(), "node.log")
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				opts := LogsOpts{Tail: tail, Format: format}
				want := opts.applyClientFilters([]byte(data))
				if strings.HasPrefix(data, "{") {
					var rendered bytes.Buffer
					if err := renderJSONLStream(bytes.NewReader(want), opts, &rendered); err != nil {
						t.Fatal(err)
					}
					want = rendered.Bytes()
				}
				var got bytes.Buffer
				if err := writeFile(path, opts, &got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got.Bytes(), want) {
					t.Fatalf("tail=%d format=%s inputbytes=%d: got %q want %q", tail, format, len(data), got.Bytes(), want)
				}
			}
		}
	}
}

func TestLocalTailKeepsGrepAndLineRangeBeforeSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.log")
	data := []byte("keep first\nsecond\nkeep third\nlast\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []LogsOpts{{Tail: 1, Grep: "keep"}, {Tail: 1, Lines: "1:2"}, {Tail: 1, Grep: "keep", Lines: "1:1"}, {Head: 2}, {Tail: 1, Head: 2}} {
		var got bytes.Buffer
		if err := writeFile(path, opts, &got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), opts.applyClientFilters(data)) {
			t.Fatalf("opts=%+v got %q", opts, got.String())
		}
	}
}

func BenchmarkShortLocalTail(b *testing.B) {
	path := filepath.Join(b.TempDir(), "large.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("ordinary log line\n", 1<<20)), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := writeFile(path, LogsOpts{Tail: 40, Format: "plain"}, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func TestEnvelopeTailKeepsEventFilteringOrder(t *testing.T) {
	paths := PathsAt(t.TempDir())
	path := paths.EnvelopeLog("fixture")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"event\":\"run_start\"}\n{\"event\":\"exec_line\",\"msg\":\"body\"}\n{\"event\":\"node_end\"}\n{\"event\":\"exec_line\",\"msg\":\"last body\"}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []LogsOpts{{Tail: 1, JSON: true}, {Tail: 1, JSON: true, EventsOnly: true}, {Tail: 1, JSON: true, Grep: "run_start"}, {Tail: 1, JSON: true, Lines: "1:2"}} {
		input := data
		if opts.EventsOnly {
			input = filterEventsOnly(input)
		}
		want := opts.applyClientFilters(input)
		var got bytes.Buffer
		if err := writeLogsFromEnvelope(paths, "fixture", opts, &got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("opts=%+v got %q want %q", opts, got.Bytes(), want)
		}
	}
}

func TestLocalTailRetainsLongJSONRecordLimit(t *testing.T) {
	data := []byte("{\"msg\":\"" + strings.Repeat("x", 4*1024*1024) + "\"}\n{\"msg\":\"last\"}\n")
	path := filepath.Join(t.TempDir(), "long.log")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []int{1, 2} {
		opts := LogsOpts{Tail: tail, JSON: true}
		var want, got bytes.Buffer
		wantErr := renderJSONLStream(bytes.NewReader(opts.applyClientFilters(data)), opts, &want)
		gotErr := writeFile(path, opts, &got)
		if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("tail=%d errors got=%v want=%v", tail, gotErr, wantErr)
		}
	}
}

func TestLocalTailTruncatedSnapshotRewinds(t *testing.T) {
	for _, length := range []int64{0, 6} {
		path := filepath.Join(t.TempDir(), "truncated.log")
		original := []byte("first\nsecond\nlast\n")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
		if err := f.Truncate(length); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Seek(2, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		if err := seekLocalTail(f, int64(len(original)), 1); err != nil {
			t.Fatal(err)
		}
		position, err := f.Seek(0, io.SeekCurrent)
		if err != nil || position != 0 {
			t.Fatalf("fallback position=%d err=%v", position, err)
		}
		got, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		opts := LogsOpts{Tail: 1}
		if !bytes.Equal(opts.applyClientFilters(got), opts.applyClientFilters(original[:length])) {
			t.Fatalf("truncated suffix: %q", got)
		}
	}
}
