package nativehost_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

type jobIDVectors struct {
	Valid   []string `json:"valid"`
	Invalid []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

func loadJobIDVectors(t *testing.T) jobIDVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../../internal/nodeproto/testdata/job-ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors jobIDVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func checkJobIDVectors(t *testing.T, plan func(t *testing.T, id string) json.RawMessage) {
	vectors := loadJobIDVectors(t)
	validate := func(raw json.RawMessage) error {
		_, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Secrets: fixtureSecrets{}, Placement: nodeproto.PlacementLocal})
		return err
	}
	for _, id := range vectors.Valid {
		if err := validate(plan(t, id)); err != nil {
			t.Errorf("job ID %q refused: %v", id, err)
		}
	}
	for _, bad := range vectors.Invalid {
		err := validate(plan(t, bad.ID))
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid || !strings.Contains(failure.Message, "jobs[0].id") || !strings.Contains(failure.Message, bad.Reason) {
			t.Errorf("job ID %q: got %v, want plan_invalid at jobs[0].id naming %q", bad.ID, err, bad.Reason)
		}
	}
}

func TestJobIDVectorsGo(t *testing.T) {
	checkJobIDVectors(t, func(t *testing.T, id string) json.RawMessage {
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, id, func(context.Context) error { return nil })
		return mustJSON(t, sdkserve.DefaultOperations.Document(plan))
	})
}

func TestJobIDVectorsTypeScript(t *testing.T) {
	node, root := nativeSDKFixture(t)
	checkJobIDVectors(t, func(t *testing.T, id string) json.RawMessage {
		h := startSDKAt(t, node, root, "job-ids.mjs")
		var hello json.RawMessage
		h.call(nodewire.Request{ID: "hello", Op: nodewire.OpHello, Protocol: nodewire.Version, Engine: &nodewire.EngineIdentity{Version: "fixture"}, Runner: &nodewire.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, &hello)
		var plan json.RawMessage
		h.call(nodewire.Request{ID: "plan", Op: nodewire.OpPlan, Pipeline: "job-ids", Args: map[string]string{"id": id}, Run: &nodewire.RunContext{RunID: "r", Pipeline: "job-ids"}}, &plan)
		h.finish()
		return plan
	})
}
