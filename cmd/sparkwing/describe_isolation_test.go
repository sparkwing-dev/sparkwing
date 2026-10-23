package main

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestPipelineSelectionRefusesOnlyItsOwnDescribeFailure(t *testing.T) {
	cause := &apierr.Error{Code: apierr.PlanFailed, Message: "describe pipeline broken: missing deployment setting"}
	schemas := []nodeproto.PipelineDescription{
		{Name: "Broken", Args: []nodeproto.ArgumentDescription{}, Error: cause},
		{Name: "Build", Args: []nodeproto.ArgumentDescription{}, Secrets: []nodeproto.SecretDeclaration{{Name: "BUILD_TOKEN"}}},
	}
	healthy, err := boundPipelineSchema(schemas, clisurface.PipelineIdentity{Name: "build", Entrypoint: "Build"})
	if err != nil || len(healthy.Secrets) != 1 || healthy.Secrets[0].Name != "BUILD_TOKEN" {
		t.Fatalf("healthy declaration lost: %+v: %v", healthy, err)
	}
	if _, err := boundPipelineSchema(schemas, clisurface.PipelineIdentity{Name: "broken", Entrypoint: "Broken"}); !errors.Is(err, cause) {
		t.Fatalf("failed pipeline lost its cause: %v", err)
	}
}
