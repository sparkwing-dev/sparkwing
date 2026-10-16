package localcontroller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func completeServiceFixture(_ context.Context, p capability.Principal, job nodeproto.JobSpec) (nodehost.ProcessServices, error) {
	return nodehost.ProcessServices{
		RunID: p.Capability.Binding.RunID, Job: job,
		Annotation: func(context.Context, capability.Authorized, nodeproto.AnnotationRequest) error { return nil },
		Summary:    func(context.Context, capability.Authorized, nodeproto.SummaryRequest) error { return nil },
		Secret:     func(context.Context, capability.Authorized, string) (string, bool, error) { return "", false, nil },
		Output: func(context.Context, capability.Authorized, string) (json.RawMessage, error) {
			return json.RawMessage(`null`), nil
		},
		Latest: func(context.Context, capability.Authorized, string, string, time.Duration) (nodehost.ProcessOutput, error) {
			return nodehost.ProcessOutput{}, nil
		},
		OIDC: func(context.Context, capability.Authorized, string) (nodehost.ProcessOIDCToken, error) {
			return nodehost.ProcessOIDCToken{}, nil
		},
		StartChild: func(context.Context, capability.Authorized, nodehost.ChildRequest) (string, error) {
			return "child", nil
		},
		AwaitChild: func(context.Context, capability.Authorized, string, string, time.Duration) (nodehost.ProcessChildResult, error) {
			return nodehost.ProcessChildResult{}, nil
		},
	}, nil
}
