# W9 continuation handoff

W9 remains incomplete. This continuation starts at `376b8902b`. The lead owns
commits, review, pipelines and native acceptance. No git write, Sparkwing
executable, service, external file or harness memory was changed.

## Earlier work retained

The earlier commits supply final grammar validation, help/completion/discovery,
local run and trigger parsing, machine-only connection profiles, the authenticated
engine transport, public run observations, listing/prune/log/review adapters,
statistics, sizing reads, pools, distinct team/deployment token managers,
dashboard lifecycle adapters and staged upgrade verification. Do not redo them.
The remaining work below includes their unfinished runtime integrations.

## Implemented in this continuation

### Secrets

`SecretManager` supplies final typed management on existing C2 secret routes.
The CLI uses it through the common profile/api.sock connection selection. The
old pflag handlers, direct legacy client calls, local health compatibility probe
and local-secret import tests are deleted.

Writes preserve name, exact stdin/file/value bytes, explicit empty values,
trailing newlines, pipeline/shared visibility, masked versus variable and
`only_on` pool. Local unscoped writes remain shared with local pipelines. Remote
writes retain their explicit visibility. The controller derives ownership and
encrypts. The client requires bound metadata and confirmation of the requested
visibility and restriction before success. Metadata/output types cannot echo
submitted values. Write and rotation retries retain body and header key.

List output is metadata NDJSON or plain names. Search is case-insensitive. Get
is an explicit authorized value read: JSON includes metadata/value, and
pretty/plain preserve raw bytes without an added newline. A scoped read may
fall back to a shared unscoped row. Deletion selects the exact row, without that
fallback, and preserves deleted versus absent. Rotation preserves counts,
replay and skipped name/pipeline pairs. Pretty output never says the previous
key can be dropped when a row was skipped.

W9's `SecretWrite`, `SecretMetadata` and result shapes complete the body/response
contract on existing routes. No C2 route or grant changed. W1/W2 must implement
those projections, `only_on` persistence, encryption, preflight and restricted
release, including here refusal. Fake tests establish none of those mutations
or security properties on a real server.

### Runner observations

`RunnerReader` uses `GET /api/v1/runners` for local and remote observations.
`RunnerDetail` separates granted name/class/location and reserved pools from
free labels. It keeps repository fences, metering, total capacity, maximum
concurrency, active run/job IDs, status, last seen and optional timestamped
headroom with queue depth. The client rejects missing/duplicate identities,
invalid measurements and timestamps. Future class/status values stay readable.

CLI JSON retains details, plain emits names, and quiet JSON emits name objects.
The dispatcher no longer calls `/agents`. Runner enrollment/service installation
still uses the old implementation and needs W6/W8 integration. W2 must serve the
new projection on the existing final route.

### Profile revocation and dead probes

Profile removal uses final `TokenManager` metadata and confirmed revocation.
Only a `member` kind permits automatic revocation. Principal spelling is no
longer interpreted as a credential kind. Failed lookup or unconfirmed revocation
retains the saved profile. Machine credentials require `--keep-token`.

The unreachable cluster-health report, fleet/trigger probes, old profile
log/gitcache probes and optional `/auth/whoami` request are deleted. Connection
auth probes use the existing final runs read route. They no longer claim a
successful identity lookup when that endpoint is absent.

## Deleted

This continuation removes **1,384 tracked Go lines gross**, from
`git diff --numstat -- '*.go'` against its starting HEAD. This includes moved
and replaced code, not a net-reduction claim. Entire deleted files:

- `cmd/sparkwing/agents.go`: 206 lines.
- `cmd/sparkwing/health.go`: 251 lines.
- `cmd/sparkwing/secret.go`: 364 lines.
- `cmd/sparkwing/secret_local_test.go`: 252 lines.

The remainder removes dead probe helpers/tests, obsolete command pointers,
legacy profile-kind decoding and the unused `Client.RotateSecrets`,
`SecretRotation`, `SecretRotationSkip` API. The API snapshot is regenerated.
No caller in another owner's production package needed migration.

Legacy `Client` secret CRUD still has W2/W3 test callers and its execution read
has a live `internal/localsecrets` caller. Runner mint/revoke still uses older
credential APIs. They remain active migration work, not forwarding shims added
by this continuation.

## Acceptance and verification

The new supplemental cells pass:

- `TestSurfaceSecretsTransport`, using api.sock/remote URL fixtures through the
  real HTTP client, including restriction/value separation and mutation results.
- `TestSurfaceRunnerTransportPreservesGrantedAndObservedFacts`, preserving all
  runner observations through the same two transports.

Full surface/native product scenarios remain skipped because the native driver
and final owner implementations are incomplete. No fake-backed cell was marked
as native evidence. T1's parked-test inventory assigns no source test directly
to W9.

Ported the CLI input/visibility/variable parts of the old local-secret tests,
profile-revocation safety, health/auth probe behavior and secret rotation's
skipped-row reporting. Old dotenv/settings migration tests were deleted under
the hard-cut decisions. Real encrypted-store and restricted-release proof remains
W1/W2 acceptance work. New regressions cover exact empty/newline input, scoped
fallback versus exact deletion, no secret value in metadata, confirmation of
restriction/encryption, replay keys/bodies, malformed responses, runner details,
quiet JSON, command dispatch and member-kind revocation without principal
heuristics. Changed health/auth tests use fake HTTP transports without listeners.

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false`, with `GOCACHE`,
`GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W9/`. Recreate cache/tmp children to
repeat them. `go test` uses `-p 4` at most.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` for cmd/sparkwing, clisurface, controllerwire,
  controller/client and tests/acceptance.
- Focused CLI/client tests selected by `^TestSecretManagement|^TestRunnerListing|
  ^TestRunnerReader|^TestProfiles(Add|Remove)|^TestProbe(Auth|Controller)|
  ^TestInterpretHealthBody|^TestCommandReferenceMatchesRegistry|^TestToken|
  ^TestCredential|^TestRenderTokens|^TestPool|^TestRunStatistics|^TestSizing|
  ^TestEngine`.
- Full clisurface, controllerwire and tests/acceptance package suites, retaining
  the skipped native product scenarios.
- `TestPkgDocsContentMatchesDocsRoot` and `TestEmbeddedChangelogMatchesRoot`.
- `sync-docs.sh --check`, commentcheck and sleepcheck against HEAD, and
  `git diff --check`.

The lead must run pipelines, full legacy package suites and native Linux/macOS/
Windows, HTTP/socket, controller/service and launch QA. Those were not run here.
The full docs suite previously hit the sandbox's listener restriction and was
not retried unchanged. The focused drift tests pass.

## Work still required

1. Wire W3 hosting and W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition remain unfinished.
   Detached execution still launches the removed consumer entrypoint. Move it to
   the local-controller queue, then delete consumer/process helpers.
2. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor, schedule selectors/pause/resume and __tick.
3. Migrate runner enrollment/service config and remaining runner token callers,
   queue, cache warm, legacy client APIs and other management paths.
4. Replace profile.Load/Save and connection selection with W6 final machine
   parsing/persistence. Profile scope enrollment remains a W2 gap.
5. Finish older guides, scripts, examples and runtime tests still using removed
   vocabulary/schemas. The secret-ownership section and generated secret/runner
   references were updated, but the rest of auth.md still needs migration.
6. Run every native surface cell and launch QA after owner implementations land.

## Contract changes needed in other areas

- W1/W2: retry failed/all job selection and an authorized sizing-reset operation
  that clears learned samples/floors while preserving explicit pins.
- W2: ordered mixed trigger/job claims, profile scope enrollment, run-scoped
  annotations and ordinary-member annotation authorization. Existing gaps are
  not approved product restrictions.
- W8: runtime/unit construction, canonical logs, access-policy/install-force
  fields and effective endpoints. No W8 contract was changed.

Real integration also requires W2's C3 server projections and apierr.DecodeInner
(which is still a stub), W1/W2 restricted-secret persistence/enforcement, W7
persisted logs/cursor/final completeness and W3/W6/W8 host composition. The secret
error-decoder test uses a fake at that declared boundary.

W9-local contract additions are `SecretManager` and `RunnerReader` and their
projections, documented in c3-controller-wire.md. No other owner's contract was
edited. No new environment variable, service, timeout limit or authorization
rule was introduced. Existing 30-second secret operations and five-minute
rotation budgets remain.

## Shared files and friction

Shared changes are tests/acceptance/{secrets,runners}_contract_test.go,
.apidiff/pkg_controller_client.txt, CHANGELOG.md,
design/rewrite/contracts/c3-controller-wire.md, this handoff, pkg/docs/changelog.md
and generated mirrors for auth, cli-secrets and cli-runners. All production
changes are in W9-owned packages. No other package's caller was changed.

CLI reference pages were rendered with the existing Go test renderer because
running Sparkwing executables is forbidden for the swarm. The temporary renderer
test was removed. Mirrors were produced by sync-docs.sh. Git identity remains
Korey Gambill and core.worktree is unset. Scratch is deleted before the final
report. The lead still needs to review and commit this continuation.
