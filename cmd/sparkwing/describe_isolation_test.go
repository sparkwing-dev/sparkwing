package main

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPipelineSelectionRefusesOnlyItsOwnDescribeFailure(t *testing.T) {
	cause := &apierr.Error{Code: apierr.PlanFailed, Message: "describe pipeline broken: missing deployment setting"}
	schemas := []nodewire.PipelineDescription{
		{Name: "Broken", Args: []nodewire.ArgumentDescription{}, Error: cause},
		{Name: "Build", Args: []nodewire.ArgumentDescription{}, Secrets: []nodewire.SecretDeclaration{{Name: "BUILD_TOKEN"}}},
	}
	healthy, err := boundPipelineSchema(schemas, clisurface.PipelineIdentity{Name: "build", Entrypoint: "Build"})
	if err != nil || len(healthy.Secrets) != 1 || healthy.Secrets[0].Name != "BUILD_TOKEN" {
		t.Fatalf("healthy declaration lost: %+v: %v", healthy, err)
	}
	if _, err := boundPipelineSchema(schemas, clisurface.PipelineIdentity{Name: "broken", Entrypoint: "Broken"}); !errors.Is(err, cause) {
		t.Fatalf("failed pipeline lost its cause: %v", err)
	}
}
