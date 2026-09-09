package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestStreamsSurviveServerWriteTimeout(t *testing.T) {
	for _, kind := range []string{"logs", "raw logs", "events"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			b := &fakeBackend{
				streamLog: func(string, string) (io.ReadCloser, error) { return reader, nil },
				getRun:    func(string) (*store.Run, error) { return &store.Run{Status: "running"}, nil },
			}
			calls := 0
			b.listEvents = func(string, int64, int) ([]store.Event, error) {
				calls++
				if calls == 2 {
					return []store.Event{{Seq: 1, Kind: "late-event"}}, nil
				}
				return nil, nil
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "events" {
					serveEventsStream(b, w, r, "run")
					return
				}
				serveLogStream(b, w, r, "run", "node")
			})
			srv := httptest.NewUnstartedServer(handler)
			srv.Config.WriteTimeout = 100 * time.Millisecond
			srv.Start()
			defer srv.Close()
			defer cancel()
			if kind != "events" {
				done := make(chan struct{})
				go func() {
					defer close(done)
					if _, err := io.WriteString(writer, ": open\n\n"); err != nil {
						return
					}
					select {
					case <-time.After(250 * time.Millisecond):
					case <-ctx.Done():
						return
					}
					if _, err := io.WriteString(writer, "data: late-event\n\n"); err != nil {
						return
					}
					writer.Close()
				}()
				defer func() { reader.Close(); <-done }()
			}
			path := srv.URL
			if kind == "raw logs" {
				path += "?format=raw"
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "late-event") {
					return
				}
			}
			t.Fatalf("stream ended before late event: %v", scanner.Err())
		})
	}
}
