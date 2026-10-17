# W9 requester secrets

The requester resolves its configured names from the pipeline's describe
schema before intake. `client.ResolveLocalSecrets` reads only those names,
retains an explicitly present empty value, skips absent optional names, and
returns fixed provider-failure text. The CLI route composition supplies the
captured environment lookup.

The intake wire carries `LocalSecrets` separately from the sealed
`Request.LocalSecretNames`. Store admission copies values into a process-local
registry keyed by team and run. SQL receives only the names. Plan acceptance
requires the names to remain declared, includes values in identifier checks,
and satisfies secret preflight from the registry. Cloud rows with the same
name cannot substitute for a lost local value.

The existing secret endpoint delivers values to live declared owner or node
claims, with the same transaction fence and `secret_released` audit. Local
results are always masked. The process-host service registers a returned value
with its masker before answering the SDK. Secret replies use `no-store`.

Committed terminal transitions remove values. Rolled-back completion retains
them. Store close clears them. Exact active intake replays keep the first
values. Changed values refuse. Loss refuses both reads and intake replay.
Retries of retained local-secret seals refuse with an instruction to submit a
new run, because those seals contain only names and cannot recover values.

## Contract changes made

- C1 records `IntakeRequest.LocalSecrets`, `Request.LocalSecretNames`, volatile
  admission lifecycle, plan preflight and masked `Secret.Ephemeral` results.
- C2 records claim-only ephemeral release through the existing declared-secret
  route. No new grants or authorization routes were added.
- C3 adds the `CreateRunRequest.LocalSecrets` input field. The coordinating W9
  worker owns its contract and OpenAPI documentation.

## Evidence

Tests were written before the new APIs. The initial resolver test build failed
with `undefined: ResolveLocalSecrets`. The store tests initially encountered
concurrent worker compilation changes before the new fields could compile.

The focused store/controller/client/nodehost suite passes, covering new local
secret tests, existing secret reads and preflight, plan identifier validation,
finish rollback, and host mask-before-response behavior.

`TestRequesterLocalSecretRealIntakeClaimAndRelease` uses the real HTTP client
with an in-process HTTP transport, real store intake and owner claim, HTTP plan
acceptance, real node reconciliation and award, and both owner and node secret
release. It includes the sealed burst preferences, secret identifier refusal,
cache refusal, foreign/undeclared/stale claim refusal, and persisted projection
checks.

The requester resolver fuzz campaign passed 11,782 executions in three seconds.
The registry fuzz campaign passed four SQLite-backed executions in five seconds.
The resolver's first campaign found a test-oracle collision for the one-byte
value `a` appearing in a fixed error. The property now checks that the complete
error is independent of the provider value and retains that seed.

The focused store and client tests and real HTTP owner/node release test pass
under `-race`. Whole-repository build,
vet on store/controller/client/runspec/wire, `git diff --check`, and commentcheck
passed. No native sockets, installed CLI, cloud pods, PostgreSQL or deployment
was exercised. There is no burst-specific acceptance cell in the existing
acceptance matrix to unskip.

## Real integration points left open

DESIGN: volatile requester secret delivery across controller processes.
Intake, plan validation and secret delivery must reach the Store process that
holds the registry. A different replica or restarted process returns
`unavailable`. Cloud load balancing cannot assume that requests share a
process. The launch topology needs explicit affinity or an unpersisted secret
relay before multiple controller processes satisfy this feature. The existing
chart defaults to one controller replica, but that was not verified natively.

The coordinating worker owns requester environment capture and masking before
describe, configured-name selection, and excluding secret files from source
bundles. This slice never writes values into source storage. These paths need
the lead's native end-to-end proof together.

## Scope and friction

This slice adds 181 net production lines across new registry/resolver code and
existing intake, plan, release and lifecycle hooks. Twenty-eight existing
production lines were replaced. No legacy command or service was replaced by
this feature. The registry is required because decision 116 forbids persisting
the values. Reusing cloud encrypted-secret rows would violate that constraint.
Reverting the requester-secret fields and hooks removes the mechanism.

W9-owned production versus main, including concurrent worker files when
measured, was +17,820/-23,829, net -6,009, within the -2,900 area budget. The
coordinator owns the final combined measurement.

Shared production files touched are `internal/runspec/contracts.go`,
`internal/controllerwire/engine.go`, `pkg/controller/engine_intake.go`,
`pkg/controller/secret_reads.go`, and the store's `store.go`, `run_spec.go`,
`secrets.go`, `secret_reads.go`, `binding_secret.go`, `secret_preflight.go`,
`plan_contracts.go`, `plan_seal.go`, `expansion_accept.go`, `dialect.go`, and
`run_state.go`. New source files are `pkg/store/local_secrets.go` and
`pkg/controller/client/local_secrets.go`. Contract additions are in C1 and C2.

Concurrent work caused temporary compile failures in routing validation,
overflow API tests, claim capacity fields, and overflow-test syntax. Focused
checks were repeated only after those files changed. No git metadata was
written. Worker scratch `/tmp/sw-astra-W9/secrets` was removed. The shared build
cache was retained.

STATUS: CONTINUE
