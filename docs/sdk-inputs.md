<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/inputs`

Package inputs builds cache keys from files, environment variables, and constants.

Import as `swinputs "github.com/sparkwing-dev/sparkwing/sparkwing/inputs"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func Compose(resolvers ...sparkwing.CacheKeyFn) sparkwing.CacheKeyFn` -- Compose combines keys with sparkwing.Key.
- `func Const(s string) sparkwing.CacheKeyFn` -- Const returns the supplied key.
- `func Env(names ...string) sparkwing.CacheKeyFn` -- Env hashes the named environment variables in sorted name order.
- `func Files(globs ...string) sparkwing.CacheKeyFn` -- Files hashes tracked files matching the supplied patterns.
- `func RepoFiles(options ...RepoFilesOption) sparkwing.CacheKeyFn` -- RepoFiles hashes every tracked file.
- `func Tree(root string) sparkwing.CacheKeyFn` -- Tree hashes every regular file under root and skips symlinks.

## Types

### type RepoFilesOption

```
type RepoFilesOption func(*repoFilesConfig)
```

- `func Ignore(patterns ...string) RepoFilesOption` -- Ignore excludes matching tracked paths from RepoFiles.
