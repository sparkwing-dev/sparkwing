package sparkwing

import "context"

var _ func(context.Context, *Registration) ([]SecretField, error) = InspectPipelineSecrets
