<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/cleanup`

Package cleanup lets a sparks library guarantee that a resource it starts -- a container, a cluster, a release -- is torn down if the step's node dies before the library's own cleanup runs.

Import as `swcleanup "github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Register(ctx context.Context, spec Spec) (release func(), err error)` -- Register records a cleanup that runs if the registering process exits before the library calls the release it returns.

## Types

### type Spec

Spec describes a cleanup to run if the registering node dies first.

```
type Spec struct {
    // Argv is the command that tears the resource down, run directly with
    // no shell. It MUST be idempotent -- exit zero whether or not the
    // resource still exists -- because the reaper runs it once, in a
    // process whose environment may differ from the step's, and does not
    // retry. Empty Argv makes Register a no-op.
    Argv []string
    // Description names the resource for the reaped event and for
    // `sparkwing doctor`; defaults to the command's program name when empty.
    Description string
}
```
