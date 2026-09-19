<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/planguard`

Package planguard implements the Plan() purity sentinel.

Import as `swplanguard "github.com/sparkwing-dev/sparkwing/sparkwing/planguard"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Active(ctx context.Context) bool` -- Active reports whether ctx is currently inside a Plan() call.
- `func Guard(ctx context.Context, what string)` -- Guard panics if invoked from inside a Pipeline.Plan() call.
- `func With(ctx context.Context) context.Context` -- With returns ctx marked as a Plan() invocation context.
