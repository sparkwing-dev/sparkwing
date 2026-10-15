package client

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

func TestNodeLogReaderUsesJobRoutes(t *testing.T) {
	for _, operation := range []string{"read", "stream", "completeness", "run"} {
		t.Run(operation, func(t *testing.T) {
			transport := transientRoundTripper(func(r *http.Request) (*http.Response, error) {
				path := "/api/v1/runs/run%2Fone/jobs/job%2Fone/logs"
				body := "masked log"
				switch operation {
				case "run":
					path = "/api/v1/runs/run%2Fone/logs"
				case "stream":
					path += "/stream"
				case "completeness":
					path += "/completeness"
					body = `{"state":"complete"}`
				}
				if r.Method != "GET" || r.URL.EscapedPath() != path || r.Header.Get("Authorization") != "Bearer private" {
					t.Fatalf("request = %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			reader := NewWithToken("https://controller.invalid", &http.Client{Transport: transport}, "private").NodeLogs()
			var err error
			switch operation {
			case "read":
				_, err = reader.Read(t.Context(), "run/one", "job/one", storage.ReadOpts{})
			case "run":
				_, err = reader.ReadRun(t.Context(), "run/one")
			case "stream":
				var body io.ReadCloser
				body, err = reader.Stream(t.Context(), "run/one", "job/one")
				if body != nil {
					body.Close()
				}
			case "completeness":
				_, _, err = reader.NodeLogCompleteness(t.Context(), "run/one", "job/one", logseg.NodeProgress{}, time.Time{})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
