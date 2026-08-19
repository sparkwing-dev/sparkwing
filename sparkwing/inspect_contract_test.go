package sparkwing

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

var _ func(context.Context, *Registration, *pipelines.Pipeline) ([]SecretField, error) = InspectPipelineSecrets
