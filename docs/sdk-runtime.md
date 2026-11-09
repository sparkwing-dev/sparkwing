<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/runtime`

Package sdkserve serves the SDK protocol without importing author or engine implementations.

Import as `swsdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func DeclarationContext(ctx context.Context) context.Context` -- DeclarationContext returns ctx marked as a Plan() invocation context.
- `func EncodeServiceRequest(body any) ([]byte, error)` -- EncodeServiceRequest encodes the body sent to a node-host service.
- `func Helper(ctx context.Context, operation string, args []any, result any) error` -- Helper sends a typed operation to the bound host.
- `func HelperStep(ctx context.Context) string` -- HelperStep returns the helper's active step, if any.
- `func Main(cfg Config) int` -- Main serves the inherited private pipes and returns the process exit code.
- `func Planning(ctx context.Context) bool` -- Planning reports whether ctx is inside a Plan() call.
- `func RequireExecution(ctx context.Context, what string)` -- RequireExecution panics if invoked from inside a Pipeline.Plan() call.
- `func SecretIsVariable(ctx context.Context) bool` -- SecretIsVariable reports which classification the client requested.
- `func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error` -- Serve negotiates once and joins every request when its input closes.
- `func WithHelperStep(ctx context.Context, step string) context.Context` -- WithHelperStep carries the active SDK step into host helper logs.
- `func WithHostExec(ctx context.Context, run HostExecFunc) context.Context` -- WithHostExec installs run for the SDK's command helpers.
- `func WithSecretClass(ctx context.Context, variable bool) context.Context` -- WithSecretClass identifies the requested value classification for host enforcement.
- `func WithServices(ctx context.Context, s Services) context.Context` -- WithServices binds services to one request; fixtures never replace process globals.

## Types

### type ChildRequest

ChildRequest carries the author's requested child without granting repository access.

```
type ChildRequest struct {
    Detached bool              `json:"detached,omitempty"`
    Pipeline string            `json:"pipeline"`
    Job      string            `json:"job"`
    Args     map[string]string `json:"args"`
    Repo     string            `json:"repo,omitempty"`
    Branch   string            `json:"branch,omitempty"`
}
```


### type Config

Config supplies the SDK operations and identity for one serving process.

```
type Config struct {
    SDKVersion string
    Operations Operations
}
```


### type HelperError

HelperError preserves host error classes across the transport.

```
type HelperError struct{ Code, Message string }
```

- `func (e *HelperError) Error() string`
- `func (e *HelperError) Is(target error) bool`

### type HostExecFunc

HostExecFunc runs a command through the host that said hello.

```
type HostExecFunc func(context.Context, nodeproto.ExecRequest) (HostExecResult, error)
```

- `func HostExec(ctx context.Context) (HostExecFunc, bool)` -- HostExec returns the command runner bound to the current host session.

### type HostExecResult

HostExecResult is the host's final word on a command.

```
type HostExecResult struct {
    Done           nodeproto.ExecDone
    Stdout, Stderr []byte
}
```


### type Operations

Operations keeps author code out of the transport's dependency graph.

```
type Operations interface {
    Context(context.Context, nodeproto.RunContext, nodeproto.RunnerIdentity, Services) context.Context
    Document(any) nodeproto.PlanDoc
    Arguments(any) []nodeproto.ArgumentDescription
    Describe(context.Context) (nodeproto.DescribeDoc, error)
    Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error)
    RunStep(context.Context, any, nodeproto.Request) (nodeproto.RunResult, error)
    Eval(context.Context, any, nodeproto.Request) (nodeproto.EvalResult, error)
}
```

```
var DefaultOperations Operations
```


### type Services

Services is the request-scoped host boundary shared by the real and fake transports.

```
type Services interface {
    Secret(context.Context, string) (string, bool, error)
    Output(context.Context, string) (json.RawMessage, error)
    Latest(ctx context.Context, pipeline, job string, maxAge time.Duration, fromAnyRun, fromProvenRun bool) (json.RawMessage, error)
    RunPipeline(context.Context, ChildRequest) (json.RawMessage, error)
    OIDC(context.Context, string) (string, error)
    Mask(context.Context, string) error
    Emit(nodeproto.FrameLog)
}
```

- `func ServicesFrom(ctx context.Context) Services` -- ServicesFrom refuses missing host services instead of consulting environment credentials.
