package client

import (
	"encoding/json"
	"net"
	"net/http"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestHeldChildReadsSurviveControllerRestart(t *testing.T) {
	for _, kind := range []string{"status", "output"} {
		for _, failure := range []string{"transport", "unavailable"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := time.Now()
					calls := 0
					var result any = controllerwire.RunDetail{ID: "child", Status: "success"}
					if kind == "output" {
						result = controllerwire.JobOutput{Output: json.RawMessage(`"finished"`)}
					}
					body, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					process := &NativeProcessClient{run: "parent", job: "await", transport: testEngine(t, func(request *http.Request) (*http.Response, error) {
						calls++
						if request.Method != http.MethodGet {
							t.Fatalf("child observation replayed %s", request.Method)
						}
						if time.Since(started) < 6*time.Second {
							if failure == "unavailable" {
								return engineResponse(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"controller restarting"}}`), nil
							}
							return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
						}
						return engineResponse(http.StatusOK, string(body)), nil
					})}
					if kind == "status" {
						_, err = process.Child(t.Context(), "child", time.Second)
					} else {
						_, err = process.ChildOutput(t.Context(), "child", "result")
					}
					if err != nil || time.Since(started) < 6*time.Second || calls < 2 {
						t.Fatalf("child read abandoned held work after %s (%d requests): %v", time.Since(started), calls, err)
					}
				})
			})
		}
	}
}

func TestHereChildIntakeRemainsSingleShotDuringOutage(t *testing.T) {
	calls := 0
	process := &NativeProcessClient{run: "parent", job: "await", transport: testEngine(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
	})}
	process.request.Options.Runner.Set = true
	process.request.Options.Runner.Value.Kind = runspec.RunnerHere
	_, err := process.StartChild(t.Context(), "child", nil, "", "")
	if err == nil || calls != 1 {
		t.Fatalf("unsafe child intake was replayed: calls=%d error=%v", calls, err)
	}
}
