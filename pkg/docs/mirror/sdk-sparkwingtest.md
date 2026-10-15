<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/sparkwingtest`

Package sparkwingtest supplies a fake protocol host without a controller or store.

Import as `swsparkwingtest "github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Types

### type Host

safety: Host exercises author code through the same plan and operation contract as a real host.

```
type Host interface {
    Context(context.Context) context.Context
    Plan(context.Context, string, map[string]string) (nodeproto.PlanDoc, error)
    RunStep(context.Context, *sparkwing.Plan, string, string) (json.RawMessage, error)
    Eval(ctx context.Context, plan *sparkwing.Plan, jobID, closureID string, input json.RawMessage) (json.RawMessage, error)
    // Records returns isolated log snapshots with numeric attributes represented as json.Number.
    Records() []sparkwing.LogRecord
    Masks() []string
    // Close cancels and joins active operations and service callbacks. Call it outside those calls.
    Close() error
}
```

- `func New(opts Options) Host` -- New snapshots option values so later caller edits cannot change an existing fixture.

### type Options

safety: Options injects external results without exporting runtime plumbing from the author SDK.

```
type Options struct {
    Runner      sparkwing.RunnerInfo
    Run         sparkwing.RunContext
    Secrets     map[string]Secret
    Outputs     map[string]json.RawMessage
    OIDCToken   func(context.Context, string) (string, error)
    Latest      func(context.Context, string, string) (json.RawMessage, error)
    RunPipeline func(context.Context, string, map[string]string) (json.RawMessage, error)
}
```


### type Secret

safety: Secret preserves masking classification so tests cannot substitute plain variables for secrets.

```
type Secret struct {
    Value  string
    Masked bool
}
```
