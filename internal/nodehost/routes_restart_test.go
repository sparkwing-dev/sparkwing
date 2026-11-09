package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestSDKChildAwaitReconnectsThroughParentAuthorization(t *testing.T) {
	s := start(t)
	service := newService(t)
	var outage atomic.Bool
	var starts atomic.Int32
	service.StartChild = func(context.Context, capability.Authorized, ChildRequest) (string, error) {
		starts.Add(1)
		outage.Store(true)
		timer := time.AfterFunc(6*time.Second, func() { outage.Store(false) })
		t.Cleanup(func() { timer.Stop() })
		return "child", nil
	}
	service.AwaitChild = func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessChildResult, error) {
		return ProcessChildResult{RunID: "child", Status: "success", Output: json.RawMessage(`"finished"`)}, nil
	}
	bearer := admitService(t, s, service)
	parent := s.parent
	private, err := client.NewLocalHostClient(client.EngineOptions{
		URL: "http://controller", Bearer: func(context.Context) (string, error) { return "parent-award", nil },
		HTTP: &http.Client{Transport: stateRestartTransport(func(request *http.Request) (*http.Response, error) {
			if outage.Load() {
				return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
			}
			var result any
			if strings.HasSuffix(request.URL.Path, "/authentication") {
				var input localhostwire.LocalAuthenticationRequest
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					return nil, err
				}
				snapshot, err := parent.ReadAuthentication(request.Context(), input.Lookup, input.Evidence)
				if err != nil {
					return nil, err
				}
				response := localhostwire.LocalAuthentication{Snapshot: snapshot}
				for _, authority := range snapshot.Authorities {
					response.Digests = append(response.Digests, authority.Principal.Capability.Digest)
				}
				result = response
			} else {
				result = localhostwire.LocalBindingChecked{Checked: true}
			}
			body, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		})},
	}, service.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := private.Close(); err != nil {
			t.Error(err)
		}
	}()
	s.authorities.deps.Parent, s.authorities.deps.Bindings = private, private
	host := httptest.NewServer(s.handler)
	defer host.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	operations := routerSDK{body: func(ctx context.Context, services sdkserve.Services) error {
		output, err := services.RunPipeline(ctx, sdkserve.ChildRequest{Pipeline: "child", Job: "result"})
		if err == nil && string(output) != `"finished"` {
			err = errors.New("child output changed")
		}
		completed <- err
		return err
	}}
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	var frames bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- sdkserve.Serve(ctx, input, &frames, sdkserve.Config{SDKVersion: "v1", Operations: operations})
	}()
	hello := map[string]any{"id": "hello", "op": "hello", "protocol": 2, "engine": map[string]string{"version": "v1"}, "runner": map[string]any{"name": "local", "type": "local", "labels": []string{}}, "binding": nodewire.ProcessBinding{RunID: service.RunID, JobID: service.Job.ID, Routes: nodewire.ProcessRoutes{URL: host.URL, Bearer: bearer}}}
	if err := json.NewEncoder(writer).Encode(hello); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, `{"id":"plan","op":"plan","pipeline":"release","args":{},"run":{"run_id":"run-1","pipeline":"release"}}`+"\n"+`{"id":"step","op":"run_step","job":"deploy","step":"body","attempt":1,"settled":[]}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Errorf("SDK child await failed through controller restart: %v", err)
		}
	case <-ctx.Done():
		t.Error("SDK child await did not reconnect:", ctx.Err())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("SDK replayed child creation %d times", starts.Load())
	}
}
