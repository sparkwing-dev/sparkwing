package logs_test

import (
	"bufio"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestStreamRestartsAfterFileShrinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "runs", "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node.log")
	if err := os.WriteFile(path, []byte("first-long-line\nstale-partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := logs.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	body, err := logs.NewClient(server.URL, nil).Stream(ctx, "run", "node")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		if scanner.Text() == "data: first-long-line" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			if scanner.Text() != "data: new" {
				t.Fatalf("old partial bytes leaked into replacement: %q", scanner.Text())
			}
			return
		}
	}
	t.Fatalf("stream missed replacement file: %v", scanner.Err())
}
