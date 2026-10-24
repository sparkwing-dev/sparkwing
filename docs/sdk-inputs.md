<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/inputs`

Package inputs declares memo inputs for evaluation by the executing host.

Import as `swinputs "github.com/sparkwing-dev/sparkwing/sparkwing/inputs"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Compose(inputs ...sparkwing.MemoInput) sparkwing.MemoInput` -- Compose combines recipes in order.
- `func Const(value string) sparkwing.MemoInput` -- Const declares a nonempty literal string, validated by the host.
- `func Env(names ...string) sparkwing.MemoInput` -- Env declares effective job environment names.
- `func Files(patterns ...string) sparkwing.MemoInput` -- Files declares tracked files matching patterns, using the same rules as Ignore.
- `func Parts(parts ...any) sparkwing.MemoInput` -- Parts declares ordered JSON values for host hashing.
- `func RepoFiles(options ...RepoFilesOption) sparkwing.MemoInput` -- RepoFiles declares all tracked files, with optional exclusions.
- `func Tree(root string) sparkwing.MemoInput` -- Tree declares regular files recursively, skipping symlinks.

## Types

### type RepoFilesOption

```
type RepoFilesOption func(*sparkwing.MemoInput)
```

- `func Ignore(patterns ...string) RepoFilesOption` -- Ignore excludes matching tracked paths.
