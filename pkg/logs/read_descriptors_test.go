//go:build linux || darwin

package logs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
