package client

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestEngineNoReplayPolicySurvivesHTTPReuse(t *testing.T) {
	checkNonReplayableHTTPReuse(t, "stable-key")
}

func FuzzEngineNoReplayPolicySurvivesHTTPReuse(f *testing.F) {
	f.Add("private-handoff")
	f.Fuzz(func(t *testing.T, key string) {
		checkNonReplayableHTTPReuse(t, "key-"+hex.EncodeToString([]byte(key)))
	})
}

func checkNonReplayableHTTPReuse(t *testing.T, key string) {
	t.Helper()
	for name, call := range map[string]func(context.Context, controllerwire.Controller) error{
		"here intake": func(ctx context.Context, c controllerwire.Controller) error {
			request := controllerwire.CreateRunRequest{IdempotencyKey: key}
			request.Request.Options.Runner.Set = true
			request.Request.Options.Runner.Value.Kind = runspec.RunnerHere
			_, err := c.CreateRun(ctx, request)
			return err
		},
		"retry intake": func(ctx context.Context, c controllerwire.Controller) error {
			_, err := c.Retry(ctx, "r1", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: key})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var awards atomic.Int32
			var serving sync.WaitGroup
			body, err := json.Marshal(controllerwire.RunDetail{ID: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
				connection, server := net.Pipe()
				serving.Add(1)
				go func() {
					defer serving.Done()
					defer server.Close()
					reader := bufio.NewReader(server)
					for {
						req, err := http.ReadRequest(reader)
						if err != nil {
							return
						}
						if _, err := io.Copy(io.Discard, req.Body); err != nil {
							t.Error(err)
							return
						}
						if err := req.Body.Close(); err != nil {
							t.Error(err)
							return
						}
						if req.Method != http.MethodGet {
							awards.Add(1)
							return
						}
						response := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(string(body)))}
						if err := response.Write(server); err != nil {
							t.Error(err)
							return
						}
					}
				}()
				return connection, nil
			}}
			t.Cleanup(func() { transport.CloseIdleConnections(); serving.Wait() })
			c, err := NewEngineClient(EngineOptions{URL: "http://controller.example", HTTP: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ReadRun(t.Context(), "r1"); err != nil {
				t.Fatal(err)
			}
			if err := call(t.Context(), c); err == nil || awards.Load() != 1 {
				t.Fatalf("award requests=%d error=%v", awards.Load(), err)
			}
		})
	}
}
