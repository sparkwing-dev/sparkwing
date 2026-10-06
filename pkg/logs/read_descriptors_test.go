//go:build linux || darwin

package logs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestLogs_ReadOfManyAttemptsHoldsOneDescriptor(t *testing.T) {
	const attempts = 300
	root := t.TempDir()
	s, err := logs.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for i := range attempts {
		line := fmt.Sprintf("attempt %03d\n", i)
		writeLogFixture(t, root, fmt.Sprintf("run/.attempts/node.log/a%03d.log", i), line)
		all.WriteString(line)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	lowered := limit
	lowered.Cur = 64
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lowered); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Error(err)
		}
	})
	handler := s.Handler()
	for _, tc := range []struct{ query, want string }{
		{query: "", want: all.String()},
		{query: "head=1", want: "attempt 000\n"},
		{query: "tail=2", want: "attempt 298\nattempt 299\n"},
		{query: "grep=attempt+15&tail=1", want: "attempt 159\n"},
		{query: "grep=attempt+299&line_numbers=1", want: `{"line_no":300,"line":"attempt 299"}` + "\n"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/logs/run/node?"+tc.query, nil))
		if w.Code != http.StatusOK || w.Body.String() != tc.want {
			t.Errorf("query %q = %d %.80q, want 200 %.80q", tc.query, w.Code, w.Body.String(), tc.want)
		}
	}
}

type blockingResponse struct {
	header  http.Header
	written chan struct{}
	resume  chan struct{}
}

func (b *blockingResponse) Header() http.Header { return b.header }
func (b *blockingResponse) WriteHeader(int)     {}
func (b *blockingResponse) Write(p []byte) (int, error) {
	if b.written != nil {
		close(b.written)
		b.written = nil
		<-b.resume
	}
	return len(p), nil
}

func TestLogs_ReadAbortsWhenALaterAttemptIsReplaced(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   string
		replace func(t *testing.T, path string)
	}{
		{name: "replaced", replace: func(t *testing.T, path string) {
			tmp := path + ".new"
			if err := os.WriteFile(tmp, []byte("other incarnation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmp, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "removed", query: "grep=attempt", replace: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s, err := logs.New(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			writeLogFixture(t, root, "run/.attempts/node.log/a1.log", strings.Repeat("first attempt\n", 1<<12))
			writeLogFixture(t, root, "run/.attempts/node.log/a2.log", "second attempt\n")
			w := &blockingResponse{header: http.Header{}, written: make(chan struct{}), resume: make(chan struct{})}
			written := w.written
			aborted := make(chan any, 1)
			go func() {
				defer func() { aborted <- recover() }()
				s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/logs/run/node?"+tc.query, nil))
			}()
			<-written
			tc.replace(t, filepath.Join(root, "runs", "run", ".attempts", "node.log", "a2.log"))
			close(w.resume)
			if got := <-aborted; got != http.ErrAbortHandler { //nolint:errorlint // net/http compares the sentinel itself
				t.Fatalf("handler finished with %v, want an aborted response", got)
			}
		})
	}
}
