<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/planguard`

Package planguard decides where a side-effect helper may run.

Import as `swplanguard "github.com/sparkwing-dev/sparkwing/sparkwing/planguard"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Grant(ctx context.Context) context.Context` -- Grant returns ctx with side-effect helpers granted, and returns a sealed ctx unchanged.
- `func Guard(ctx context.Context, helper string)` -- Guard panics unless ctx grants side effects.
- `func Seal(ctx context.Context) context.Context` -- Seal returns ctx with side-effect helpers refused, whatever ctx carried before.
