package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzLogReaderNeverFollowsRedirect(f *testing.F) {
	for _, location := range []string{"/events", "https://controller.invalid/events", "https://foreign.invalid/events", "//foreign.invalid/events", ""} {
		f.Add(location)
	}
	f.Fuzz(func(t *testing.T, location string) {
		calls := 0
		hc := &http.Client{Timeout: time.Second, Transport: engineTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls != 1 || r.URL.Host != "controller.invalid" || r.Header.Get("Authorization") != "Bearer synthetic-stream-token" {
				t.Fatal("log read left the selected controller")
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
		})}
		reader, err := NewLogReader(EngineOptions{URL: "https://controller.invalid", HTTP: hc, Bearer: func(context.Context) (string, error) { return "synthetic-stream-token", nil }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Job: "job"}); err == nil {
			t.Fatal("redirected controller read succeeded")
		}
		if calls != 1 || hc.Timeout != time.Second || hc.CheckRedirect != nil {
			t.Fatal("read retried or mutated caller transport")
		}
	})
}
