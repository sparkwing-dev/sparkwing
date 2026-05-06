# sparkwing

Pipelines as Go code. The public CLI + SDK for sparkwing.

> **Status:** pre-release. Tester binaries are available; APIs may change.
> Pre-1.0; expect churn outside the stable surface (`sparkwing/` package).

## Install

```sh
curl -fsSL https://sparkwing.dev/install.sh | sh
```

Drops `sparkwing` into `~/.local/bin` and creates a `wing -> sparkwing`
symlink. Detects your OS/arch and pulls the matching release binary
from
[GitHub Releases](https://github.com/sparkwing-dev/sparkwing/releases/latest).

For a specific version: `curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version vX.Y.Z`.
Or build from source: `go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing@latest`.

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

- **[`sparkwing/`](./sparkwing)** — the stable user-facing DSL: Plan,
  Job, Work, Step, modifiers, runtime helpers (`sparkwing.Bash`,
  `sparkwing.Path`, etc.), wire types. This is the package with
  stability guarantees.
- **Implementation packages** — `orchestrator/`, `controller/client/`,
  `bincache/`, `logs/`, `pkg/storage/`, `otelutil/`, `profile/`,
  `repos/`, `secrets/`. Exported for technical reasons (the CLI
  consumes them) but APIs may change in any release. Don't import
  them from user pipeline code.

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

## Reporting issues

Open an issue at
[github.com/sparkwing-dev/sparkwing/issues](https://github.com/sparkwing-dev/sparkwing/issues).

## License

Sparkwing is source-available under the Elastic License 2.0. You can
read, modify, and use it internally for free; you cannot resell it as
a managed service. See [LICENSE](./LICENSE).
