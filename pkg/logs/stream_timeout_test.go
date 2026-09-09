package logs_test

import (
	"bufio"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestStreamSurvivesServerWriteTimeout(t *testing.T) {
	s, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := logs.NewClient(srv.URL, nil)
	stream, err := c.Stream(ctx, "run", "node")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := c.Append(ctx, "run", "node", []byte("late-line\n")); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "late-line") {
			return
		}
	}
	t.Fatalf("stream ended before the first 200ms poll: %v", scanner.Err())
}
