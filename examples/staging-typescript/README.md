# TypeScript staging integration gate

This twin has passed strict source typechecking and SDK protocol checks for
matrix outputs, typed outputs, predecessor settlement, generated child replay,
optional capability skips and OIDC signature verification. It has not completed
an integration run through the candidate CLI.

Ordinary CLI execution is blocked in this checkout. The checked-in TypeScript
SDK has version `0.0.0`, so the local file dependency selects engine `v0.0.0`.
The CLI requires a matching verified engine for TypeScript pins. Its installed
candidate engine bypass currently supports Go replacements only.

Before running this project through the CLI, use a release-stamped local SDK
package or the corresponding published `@sparkwing/sdk` release, update this
project's dependency and exact npm lock resolution, and make the matching
verified Sparkwing engine available. A source checkout with an unstamped SDK
does not satisfy that prerequisite. Once those prerequisites are met, the
project is selected with `-C examples/staging-typescript` and its pipeline is
named `example-ts`.

The jobs assert matrix fan-out, typed job and predecessor outputs, generated
children, scoped secrets, OIDC claims and signatures, dependency cache restore,
memoized output, artifact delivery, Docker output, hooks, approval timeout,
optional failures, skip conditions and collected failures. Independent jobs run
in parallel. Secrets, OIDC and Docker report skip reasons when unavailable.

Lifecycle jobs drive the Go `example-probe` through the CLI and read the API's
run history and logs. `--go-checkout` defaults to this repository's root.
`--cli` selects the CLI binary, and `--api-profile` selects a controller profile
instead of the local controller. Add `--fail-once` to assert failed-only retry.
The input `--secret-name` selects the optional declared `EXAMPLE_GATE_SECRET`.
The Docker job runs on Cloud or runners labeled `docker` or `docker=true`.

After the assertions, `reference-pipeline-assert` can trigger a separate run in
`sparkwing-ci-smoke`. Supply `--smoke-profile` and `--smoke-checkout` to enable it.
The target pipeline defaults to `reference`; `--smoke-pipeline` overrides it.
Both `hello` and `docker-test` must succeed. Without a target profile the job
skips with a reason.

This is a standalone project because Sparkwing chooses one SDK language for
each `.sparkwing` directory. Its child `weather-report` uses the TypeScript SDK
too. The SDK supports job memoization. A memoized job can contain a single step,
but individual steps do not have a separate memo policy. The lifecycle memo
check verifies reuse through the Go fixture because the SDK has no run history
client.
