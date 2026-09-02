# sparkwing

[![CI](https://github.com/sparkwing-dev/sparkwing/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/sparkwing-dev/sparkwing/actions/workflows/ci.yaml)

Pipelines as Go code. The public CLI + SDK for sparkwing.

> **Status:** pre-release. Tester binaries are available; APIs may change.
> Pre-1.0; expect churn outside the stable surface (`sparkwing/` package).

## Install

```sh
curl -fsSL https://sparkwing.dev/install.sh | sh
```

Drops `sparkwing` into `~/.local/bin`. Detects your OS/arch and pulls
the matching release binary from
[GitHub Releases](https://github.com/sparkwing-dev/sparkwing/releases/latest).

For a specific version: `curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version vX.Y.Z`.

Building from source via `go install` is supported, but the Next.js
dashboard bundle is a generated artifact and is not checked into the
repository, so a source build will refuse to start `sparkwing dashboard`
with a clear message. Use a release binary for the dashboard, or
generate the bundle locally first (`bash bin/build-web.sh && go install
./cmd/sparkwing` from a sparkwing checkout).

## Quick start

```sh
mkdir my-pipelines && cd my-pipelines
git init
sparkwing pipeline new --name hello

# Edit .sparkwing/jobs/hello.go to taste, then run it
sparkwing run hello

# Open the local dashboard
sparkwing dashboard start
```

`sparkwing info` surveys the current repo and suggests next commands.
`sparkwing docs list` browses the embedded reference (offline,
version-locked).

## What this module is

The Go module pipeline authors import:

- **[`sparkwing/`](./sparkwing)** -- the stable user-facing DSL: Plan,
  Job, Work, Step, modifiers, runtime helpers (`sparkwing.Bash`,
  `sparkwing.Path`, etc.), wire types. This is the package with
  stability guarantees.
- **Other `pkg/` packages** -- `pkg/controller/client`, `pkg/logs`,
  `pkg/storage`, `pkg/runner`, `pkg/wingwire` and the rest. Exported
  and covered by the same stability promise as `sparkwing/` (see
  [VERSIONING.md](./VERSIONING.md)), but they are CLI and controller
  plumbing rather than pipeline-authoring API.
- **Everything under `internal/`** -- the orchestrator, admission,
  bincache, otelutil, profile, repos, secrets and friends.
  Implementation detail that changes at any time; Go forbids importing
  it from outside this module.

## Authoring pipelines

```go
package jobs

import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type Hello struct{ sw.Base }

func (Hello) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
    sw.Job(plan, run.Pipeline, func(ctx context.Context) error {
        sw.Info(ctx, "hello, sparkwing")
        return nil
    })
    return nil
}

func init() {
    sw.Register[sw.NoInputs]("hello", func() sw.Pipeline[sw.NoInputs] { return &Hello{} })
}
```

`sparkwing pipeline new` scaffolds a working stub. See [`docs/`](./docs)
for the full reference; [`docs/sdk.md`](./docs/sdk.md) is the SDK
flat reference.

## HTTP API

The controller's HTTP API (served by `sparkwing-controller` in
cluster mode and embedded in the `sparkwing` CLI for laptop mode
via `sparkwing dashboard start`) is documented as an OpenAPI 3.0
spec at
[`api/openapi.yaml`](./api/openapi.yaml). Every route, request
shape, response shape, and security requirement is described there.
The route inventory and each route's `x-sparkwing-scope` are written
from `pkg/controller/server.go` by `bash bin/gen-api-docs.sh`, and
`bash bin/check-api-spec.sh` fails when the file and the router
disagree.

To view it: paste the file into [editor.swagger.io](https://editor.swagger.io)
for an interactive renderer, or open with any OpenAPI-aware tool
(Redoc, Stoplight, Postman, etc.). The Go client at
[`pkg/controller/client/`](./pkg/controller/client) implements the
same routes; new clients in other languages should be able to be
generated from the spec.

## Stability

Sparkwing follows semantic versioning, but with explicit scope: only
some packages and surfaces carry stability promises. The short version
is that `pkg/...`, the top-level `sparkwing/` SDK package, CLI flags,
wire formats, and YAML configs are covered; everything under
`internal/...` is implementation detail and may change at any time.

See [VERSIONING.md](./VERSIONING.md) for the full policy, the
deprecation procedure, and the pre-1.0 caveat. User-visible changes
land in [CHANGELOG.md](./CHANGELOG.md); CI enforces that covered
surfaces ship with matching entries.

## Reporting issues

Open a public issue for bugs and feature requests at
[github.com/sparkwing-dev/sparkwing/issues](https://github.com/sparkwing-dev/sparkwing/issues).
Report suspected vulnerabilities privately by following the
[security policy](./SECURITY.md).

## License

Sparkwing is source-available under the Elastic License 2.0. You can
read, modify, and use it internally for free; you cannot resell it as
a managed service. See [LICENSE](./LICENSE).
