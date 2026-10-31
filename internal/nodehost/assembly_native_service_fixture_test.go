package nodehost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func completeServiceFixture(_ context.Context, p capability.Principal, job nodeproto.JobSpec) (ProcessServices, error) {
	return ProcessServices{
		RunID: p.Capability.Binding.RunID, Job: job,
		Annotation: func(context.Context, capability.Authorized, nodeproto.AnnotationRequest) error { return nil },
		Summary:    func(context.Context, capability.Authorized, nodeproto.SummaryRequest) error { return nil },
		Secret:     func(context.Context, capability.Authorized, string) (string, bool, error) { return "", false, nil },
		Output: func(context.Context, capability.Authorized, string) (json.RawMessage, error) {
			return json.RawMessage(`null`), nil
		},
		Latest: func(context.Context, capability.Authorized, string, string, time.Duration, bool) (ProcessOutput, error) {
			return ProcessOutput{}, nil
		},
		OIDC: func(context.Context, capability.Authorized, string) (ProcessOIDCToken, error) {
			return ProcessOIDCToken{}, nil
		},
		StartChild: func(context.Context, capability.Authorized, ChildRequest) (string, error) {
			return "child", nil
		},
		AwaitChild: func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessChildResult, error) {
			return ProcessChildResult{}, nil
		},
	}, nil
}
