# TypeScript staging integration gate

This twin asserts staging integration behavior through the TypeScript SDK.
Independent jobs exercise outputs, children, caches, artifacts, memo reuse and
run lifecycle operations. Secrets, OIDC and Docker report skip reasons when
unavailable.

Run the SDK protocol checks from the repository root with Node 22.18 or newer:

```bash
npm --prefix examples/staging-typescript/.sparkwing test
```

These checks drive the real SDK host with controlled services. They verify
output and generator replay, skip predicates, OIDC signature rejection, stale
cache and artifact rejection, memo reuse assertions and lifecycle cleanup
registration. Cache storage and run lifecycle still need the pipeline's
integration run.

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
native memo reuse, artifact delivery, Docker output, hooks, approval timeout,
optional failures, skip conditions and collected failures. Independent jobs run
in parallel. The cache probe clears stale local files before preparing its seed
cache. Restored cache and artifact contents must match the current run ID.

`native-memo-reuse-assert` starts the TypeScript `memo-probe` child twice with a
token unique to the parent run. The child's memoized job returns its producing
run ID. Both calls must return the same ID, proving the second child reused the
first child's result.

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
skips with a reason. The host retains cancellation cleanup for detached probes
and reference runs until the gate observes a terminal state, so an interrupted
gate does not abandon them.

This is a standalone project because Sparkwing chooses one SDK language for
each `.sparkwing` directory. Its child `weather-report` uses the TypeScript SDK
too. The SDK supports job memoization. A memoized job can contain a single step,
but individual steps do not have a separate memo policy. Lifecycle checks use
the CLI because the SDK has no run history client.
