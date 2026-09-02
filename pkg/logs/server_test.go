package logs_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func newLogsServer(t *testing.T) (*logs.Client, string, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	c := logs.NewClient(srv.URL, nil)
	return c, dir, srv.Close
}

func TestLogs_AppendReadRoundTrip(t *testing.T) {
	c, _, stop := newLogsServer(t)
	defer stop()

	ctx := context.Background()

	got, err := c.Read(ctx, "run-1", "step-a")
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}

	if err := c.Append(ctx, "run-1", "step-a", []byte("line 1\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(ctx, "run-1", "step-a", []byte("line 2\n")); err != nil {
		t.Fatal(err)
	}
	got, err = c.Read(ctx, "run-1", "step-a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "line 1\nline 2\n" {
		t.Errorf("Read: got %q", got)
	}
}

func TestLogs_FilterPreservesFinalNewline(t *testing.T) {
	c, _, stop := newLogsServer(t)
	defer stop()

	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		data   string
		filter logs.ReadFilter
		want   string
	}{
		{name: "terminated", data: "first\nsecond\n", filter: logs.ReadFilter{Head: 2}, want: "first\nsecond\n"},
		{name: "unterminated", data: "first\nsecond", filter: logs.ReadFilter{Head: 2}, want: "first\nsecond"},
		{name: "head-prefix", data: "first\nsecond", filter: logs.ReadFilter{Head: 1}, want: "first\n"},
		{name: "range-prefix", data: "first\nsecond", filter: logs.ReadFilter{Lines: "1:1"}, want: "first\n"},
		{name: "grep-prefix", data: "first\nsecond", filter: logs.ReadFilter{Grep: "first"}, want: "first\n"},
		{name: "tail-final", data: "first\nsecond", filter: logs.ReadFilter{Tail: 1}, want: "second"},
		{name: "empty-selection", data: "first\nsecond", filter: logs.ReadFilter{Grep: "absent"}, want: ""},
		{name: "single-empty-line", data: "\n", filter: logs.ReadFilter{Head: 1}, want: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Append(ctx, "run-filter", tc.name, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			got, err := c.ReadFiltered(ctx, "run-filter", tc.name, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("filtered bytes = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogs_ReadRunConcatenates(t *testing.T) {
	c, _, stop := newLogsServer(t)
	defer stop()
	ctx := context.Background()

	_ = c.Append(ctx, "run-multi", "a", []byte("A content\n"))
	_ = c.Append(ctx, "run-multi", "b", []byte("B content\n"))

	got, err := c.ReadRun(ctx, "run-multi")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	s := string(got)
	for _, want := range []string{"=== a ===", "A content", "=== b ===", "B content"} {
		if !strings.Contains(s, want) {
			t.Errorf("ReadRun output missing %q:\n%s", want, s)
		}
	}
}

func TestLogs_PathTraversalRejected(t *testing.T) {
	_, _, stop := newLogsServer(t)
	defer stop()

	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	srvURL := srv.URL

	resp, err := http.Post(srvURL+"/api/v1/logs/..%2Fescape/node",
		"text/plain", bytes.NewReader([]byte("pwn")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("path traversal status=%d want 400", resp.StatusCode)
	}
}

func TestLogs_UnsafeIDsRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	c := logs.NewClient(srv.URL, nil)
	if err := c.Append(context.Background(), "run-keep", "step-a", []byte("keep me\n")); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(dir, "runs", "run-keep", "step-a.log")

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "delete-dot-encoded", method: http.MethodDelete, path: "/api/v1/logs/%2e"},
		{name: "delete-dot-dot-encoded", method: http.MethodDelete, path: "/api/v1/logs/%2e%2e"},
		{name: "delete-parent-escape", method: http.MethodDelete, path: "/api/v1/logs/..%2Fruns"},
		{name: "read-run-dot-encoded", method: http.MethodGet, path: "/api/v1/logs/%2e"},
		{name: "read-node-outside-run", method: http.MethodGet, path: "/api/v1/logs/%2e/step-a"},
		{name: "append-node-outside-run", method: http.MethodPost, path: "/api/v1/logs/%2e/step-a"},
		{name: "append-encoded-separator", method: http.MethodPost, path: "/api/v1/logs/run-keep/%2Fetc%2Fpasswd"},
		{name: "append-node-dot", method: http.MethodPost, path: "/api/v1/logs/run-keep/%2e"},
		{name: "stream-dot-encoded", method: http.MethodGet, path: "/api/v1/logs/%2e/step-a/stream"},
		{name: "append-leading-dot", method: http.MethodPost, path: "/api/v1/logs/.hidden/step-a"},
		{name: "append-space-node", method: http.MethodPost, path: "/api/v1/logs/run-keep/%20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader("pwn"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s %s status=%d, want 400", tc.method, tc.path, resp.StatusCode)
			}
			if _, err := os.Stat(kept); err != nil {
				t.Fatalf("existing log gone after %s %s: %v", tc.method, tc.path, err)
			}
		})
	}

	for _, tc := range []struct{ name, run, node string }{
		{name: "empty-run", run: "", node: "step-a"},
		{name: "empty-node", run: "run-keep", node: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Append(context.Background(), tc.run, tc.node, []byte("pwn")); err == nil {
				t.Errorf("Append(%q, %q) succeeded, want rejection", tc.run, tc.node)
			}
		})
	}
}

func TestLogs_RealIDsAccepted(t *testing.T) {
	c, dir, stop := newLogsServer(t)
	defer stop()

	ctx := context.Background()
	for _, id := range []string{"run-20260901-120000-ab12", "run-20260901-120000-1f2e3d4c"} {
		for _, node := range []string{"build.amd64-v2", "gate_pre-commit", "a"} {
			if err := c.Append(ctx, id, node, []byte("line\n")); err != nil {
				t.Fatalf("Append(%q, %q): %v", id, node, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "runs", id, node+".log")); err != nil {
				t.Fatalf("log for (%q, %q) missing: %v", id, node, err)
			}
		}
	}
}
