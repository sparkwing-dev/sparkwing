package logs_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/logs"
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

	// Read of a nonexistent file returns empty, not error. Lets
	// callers poll before work starts without special-casing.
	got, err := c.Read(ctx, "run-1", "step-a")
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}

	// Append twice, make sure both chunks survive.
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
	// Just assert both banners + bodies appear, without asserting
	// order -- ReadDir order is fs-dependent.
	s := string(got)
	for _, want := range []string{"=== a ===", "A content", "=== b ===", "B content"} {
		if !strings.Contains(s, want) {
			t.Errorf("ReadRun output missing %q:\n%s", want, s)
		}
	}
}

// TestLogs_PathTraversalRejected locks in the ID guard. Log
// endpoints take IDs straight from the URL; anything that could
// escape the root would be a serious bug.
func TestLogs_PathTraversalRejected(t *testing.T) {
	_, _, stop := newLogsServer(t)
	defer stop()

	// Build raw URL with the nasty payload so the client's URL
	// escaping doesn't paper over the issue; re-create the httptest
	// server directly so its base URL is exposed.
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	srvURL := srv.URL

	// `..%2Fescape` decodes to `../escape`, the guard rejects.
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
