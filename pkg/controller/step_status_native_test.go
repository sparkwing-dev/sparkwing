package controller

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestRunStepsExposePersistedFailureThroughSharedClient(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
	raw := nativeClaimPresentation(t, engine, authorized)
	server := New(engine.Store, nil)
	engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/steps/start", `{"step_id":"run","state":"running","source_attempt":1}`, 200)
	engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/steps/finish", `{"step_id":"run","state":"failed","error":"service request fields cannot be null","source_attempt":1}`, 200)
	reader, err := client.NewRunReader(client.EngineOptions{
		URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}},
		Bearer: func(context.Context) (string, error) { return raw, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"run", "1"} {
		steps, err := reader.ReadSteps(t.Context(), selector)
		if err != nil || len(steps.Steps) != 1 {
			t.Fatalf("steps = %+v, error = %v", steps, err)
		}
		step := steps.Steps[0]
		if step.JobID != "job" || step.ID != "run" || step.Attempt != 1 || step.Outcome != "failed" || step.Reason != "service request fields cannot be null" || step.StartedAt == nil || step.FinishedAt == nil {
			t.Fatalf("lost persisted step failure: %+v", step)
		}
	}
}
