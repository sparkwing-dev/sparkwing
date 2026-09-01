package stdoutlogs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/stdoutlogs"
)

func TestAppend_PrefixesEachLine(t *testing.T) {
	var buf bytes.Buffer
	ls := stdoutlogs.NewWithWriter(&buf)
	if err := ls.Append(context.Background(), "run-1", "compile", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	want := "run-1 compile | hello\nrun-1 compile | world\n"
	if buf.String() != want {
		t.Errorf("\nwant %q\ngot  %q", want, buf.String())
	}
}

func TestAppend_AppendsTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	ls := stdoutlogs.NewWithWriter(&buf)
	if err := ls.Append(context.Background(), "r", "n", []byte("nonewline")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if buf.String() != "r n | nonewline\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestAppend_EmptyDataIsNoop(t *testing.T) {
	var buf bytes.Buffer
	ls := stdoutlogs.NewWithWriter(&buf)
	if err := ls.Append(context.Background(), "r", "n", nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no write, got %q", buf.String())
	}
}

func TestAppend_RejectsMissingIDs(t *testing.T) {
	ls := stdoutlogs.New()
	if err := ls.Append(context.Background(), "", "n", []byte("x")); err == nil {
		t.Error("expected error for empty runID")
	}
	if err := ls.Append(context.Background(), "r", "", []byte("x")); err == nil {
		t.Error("expected error for empty nodeID")
	}
}

func TestAppend_ConcurrentWritesNeverTear(t *testing.T) {
	var buf bytes.Buffer
	ls := stdoutlogs.NewWithWriter(&buf)
	const (
		workers     = 8
		linesPerJob = 50
	)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		nodeID := fmt.Sprintf("job-%d", w)
		go func() {
			defer wg.Done()
			for i := range linesPerJob {
				payload := fmt.Sprintf("line-%d-with-some-padding-%s\n", i, strings.Repeat("x", 80))
				if err := ls.Append(context.Background(), "r", nodeID, []byte(payload)); err != nil {
					t.Errorf("append: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	expectedLines := workers * linesPerJob
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != expectedLines {
		t.Fatalf("got %d lines, want %d (torn writes?)", len(lines), expectedLines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "r job-") {
			t.Fatalf("line missing prefix: %q", line)
		}
		if !strings.Contains(line, " | line-") {
			t.Fatalf("line missing delimiter: %q", line)
		}
	}
}

func TestRead_ReturnsErrReadUnsupported(t *testing.T) {
	ls := stdoutlogs.New()
	_, err := ls.Read(context.Background(), "r", "n", storage.ReadOpts{})
	if !errors.Is(err, stdoutlogs.ErrReadUnsupported) {
		t.Errorf("want ErrReadUnsupported, got %v", err)
	}
}

func TestReadRun_ReturnsErrReadUnsupported(t *testing.T) {
	ls := stdoutlogs.New()
	_, err := ls.ReadRun(context.Background(), "r")
	if !errors.Is(err, stdoutlogs.ErrReadUnsupported) {
		t.Errorf("want ErrReadUnsupported, got %v", err)
	}
}

func TestStream_ReturnsErrReadUnsupported(t *testing.T) {
	ls := stdoutlogs.New()
	_, err := ls.Stream(context.Background(), "r", "n")
	if !errors.Is(err, stdoutlogs.ErrReadUnsupported) {
		t.Errorf("want ErrReadUnsupported, got %v", err)
	}
}

func TestDeleteRun_IsNoop(t *testing.T) {
	ls := stdoutlogs.New()
	if err := ls.DeleteRun(context.Background(), "anything"); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestCheckSpec_RejectsExtraFields(t *testing.T) {
	if err := stdoutlogs.CheckSpec("", "", "", "", "", ""); err != nil {
		t.Errorf("empty fields should validate, got %v", err)
	}
	if err := stdoutlogs.CheckSpec("b", "", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Errorf("want bucket error, got %v", err)
	}
	if err := stdoutlogs.CheckSpec("", "", "/tmp", "", "", ""); err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("want path error, got %v", err)
	}
}

var _ storage.LogStore = (*stdoutlogs.LogStore)(nil)
