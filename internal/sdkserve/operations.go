package sdkserve

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// Operations keeps author code out of the transport's dependency graph.
type Operations interface {
	Context(context.Context, nodeproto.RunContext, nodeproto.RunnerIdentity, Services) context.Context
	Document(any) nodeproto.PlanDoc
	Describe(context.Context) (nodeproto.DescribeDoc, error)
	Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error)
	RunStep(context.Context, any, nodeproto.Request) (nodeproto.RunResult, error)
	Eval(context.Context, any, nodeproto.Request) (nodeproto.EvalResult, error)
}

// DefaultOperations is installed once by SDK initialization, before user code runs.
var DefaultOperations Operations

// Services is the request-scoped host boundary shared by the real and fake transports.
type Services interface {
	Secret(context.Context, string) (string, bool, error)
	Output(context.Context, string) (json.RawMessage, error)
	Latest(context.Context, string, string, time.Duration) (json.RawMessage, error)
	RunPipeline(context.Context, ChildRequest) (json.RawMessage, error)
	OIDC(context.Context, string) (string, error)
	Mask(context.Context, string) error
	Emit(nodeproto.FrameLog)
}

type servicesKey struct{}

// WithServices binds services to one request; fixtures never replace process globals.
func WithServices(ctx context.Context, s Services) context.Context {
	return context.WithValue(ctx, servicesKey{}, s)
}

// ServicesFrom refuses missing host services instead of consulting environment credentials.
func ServicesFrom(ctx context.Context) Services {
	if s, ok := ctx.Value(servicesKey{}).(Services); ok {
		return s
	}
	return unavailableServices{}
}

type unavailableServices struct{}

func (unavailableServices) Secret(context.Context, string) (string, bool, error) {
	return "", false, fmt.Errorf("host secret service unavailable")
}
func (unavailableServices) Output(context.Context, string) (json.RawMessage, error) {
	return nil, fmt.Errorf("host output service unavailable")
}
func (unavailableServices) Latest(context.Context, string, string, time.Duration) (json.RawMessage, error) {
	return nil, fmt.Errorf("host reference service unavailable")
}
func (unavailableServices) RunPipeline(context.Context, ChildRequest) (json.RawMessage, error) {
	return nil, fmt.Errorf("host pipeline service unavailable")
}
func (unavailableServices) OIDC(context.Context, string) (string, error) {
	return "", fmt.Errorf("host OIDC service unavailable")
}
func (unavailableServices) Mask(context.Context, string) error {
	return fmt.Errorf("host mask service unavailable")
}
func (unavailableServices) Emit(nodeproto.FrameLog) {}

// ChildRequest carries the author's requested child without granting repository access.
type ChildRequest struct {
	Pipeline string            `json:"pipeline"`
	Job      string            `json:"job"`
	Args     map[string]string `json:"args"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}
