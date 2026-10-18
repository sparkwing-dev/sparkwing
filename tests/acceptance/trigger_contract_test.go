package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceInitialTriggerIntakeTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		for _, here := range []bool{false, true} {
			for _, pipelineSHA := range []string{"0123456789abcdef0123456789abcdef01234567", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
				env, err := runspec.EnvironmentDigest(map[string]string{"DEPLOY_REGION": "private-environment-value"}, strings.Repeat("5", 64))
				if err != nil {
					t.Fatal(err)
				}
				request := controllerwire.CreateRunRequest{IdempotencyKey: "one-intake", Request: runspec.Request{
					Version: 1, Pipeline: "deploy-prod", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
					SecretArgs: runspec.Optional[[]string]{Set: true, Value: []string{"password"}}, Env: env,
					RequestedArgs: map[string]string{"rollout.image": "release", "region": "us-east"},
					Git:           runspec.Git{Repository: runspec.Repository{Key: "gh:17", Display: "acme/app"}, Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567"},
					Source:        runspec.Source{Ref: pipelineSHA, Entrypoint: "Deploy", Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}},
				}}
				if here {
					request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
				}
				sealedInput := request.Request
				sealedInput.Team = "server-team"
				seal, err := runspec.SealRequest(sealedInput, runspec.IntakeFacts{RunID: "r17", AdmittedAt: time.Unix(1, 0)})
				if err != nil {
					t.Fatal(err)
				}
				response := controllerwire.CreateRunResponse{RunID: "r17", Number: 17, Request: seal}
				if here {
					response.HereBearer = "private-server-bearer"
				}
				raw, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					var got controllerwire.CreateRunRequest
					if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
						t.Fatal(err)
					}
					if req.Method != "POST" || req.URL.Path != "/api/v1/runs" || req.Header.Get("Idempotency-Key") != request.IdempotencyKey || !reflect.DeepEqual(got, request) {
						t.Fatalf("initial intake changed: %s %s %+v", req.Method, req.URL, got)
					}
					return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := controller.CreateRun(t.Context(), request)
				if err != nil || calls != 1 || !reflect.DeepEqual(result, response) {
					t.Fatalf("intake round trip changed: calls=%d error=%v", calls, err)
				}
			}
		}
	}
}
