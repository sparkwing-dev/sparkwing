<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/inputs`

Package inputs provides sparkwing.CacheKeyFn helpers for declaring "what changed" inputs to a node's cache.

Import as `swinputs "github.com/sparkwing-dev/sparkwing/sparkwing/inputs"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Compose(fns ...sparkwing.CacheKeyFn) sparkwing.CacheKeyFn` -- Compose folds multiple sparkwing.CacheKeyFn values into one via sparkwing.Key.
- `func Const(s string) sparkwing.CacheKeyFn` -- Const returns a sparkwing.CacheKeyFn that always returns the same value.
- `func Env(names ...string) sparkwing.CacheKeyFn` -- Env returns a sparkwing.CacheKeyFn that hashes the values of the named environment variables.
- `func Files(globs ...string) sparkwing.CacheKeyFn` -- Files returns a sparkwing.CacheKeyFn that hashes the contents of tracked files matching the given globs.
- `func RepoFiles(opts ...RepoFilesOption) sparkwing.CacheKeyFn` -- RepoFiles returns a sparkwing.CacheKeyFn that hashes the contents of every tracked file in the repo.
- `func Tree(root string) sparkwing.CacheKeyFn` -- Tree returns a sparkwing.CacheKeyFn that hashes the contents of every regular file under root, walking the directory tree without consulting git.

## Types

### type RepoFilesOption

RepoFilesOption mutates RepoFiles' behavior.

```
type RepoFilesOption func(*repoFilesConfig)
```

- `func Ignore(patterns ...string) RepoFilesOption` -- Ignore excludes paths matching any of the patterns from the RepoFiles hash.
