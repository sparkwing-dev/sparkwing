# W9 burst launch follow-through

Starting HEAD: `a669326ec`. Earlier committed routing, source, secret, placement
and overflow work is retained. No Git writes, operational Sparkwing execution,
candidate installation, services, deployment or release ran.

## Implemented

- Personal registration now works from the CLI with `--personal --session-stdin`.
  The existing independent account session and CSRF proof remain in memory.
  Real CLI/controller/store tests cover enrollment, ownership and failed-install
  revocation. A member bearer cannot become an independent machine root.
- An unreachable controller during source upload now falls back locally with a
  message. Only upload preparation carries the before-intake error mark.
  Submission failures and interrupted preparation cannot create a local copy.
- Complete source bundles acquire live signed source authority without requiring
  an unnecessary Git credential. Missing commits, tags, submodules and LFS still
  require live credential authorization before a remote fetch.
- Live pending node awards can read their sealed source bundle before an attempt
  starts, including after a previous attempt. They acquire no output, cache or
  upload identity. Foreign data failures use canonical data refusals.
- Cloud source initialization now works before bootstrap exchange. The existing
  download route has a source-only bootstrap alternative. Bootstrap grants stay
  exactly `source.read` and `claim.exchange`; secrets and general data remain
  unavailable. Node, plan and eval source reads are fenced against cancellation,
  expiry, changed holders/generations, consumption and foreign digests.
- Fresh node data scopes carry no active attempt. Authentication projects zero
  until execution starts. Transition result proof carries the decoded source
  ordinal independently. A real retry prepares source before/after exchange,
  starts ordinal 3 from historical ordinal 2, replays its exact start once and
  refuses a changed source ordinal.
- Run docs and migrations describe session enrollment and safe fallback. API,
  CLI and exported API references and embedded mirrors are regenerated. Stale
  storage/cache prose is deleted and billing/debugging enter navigation.
  Doccheck accepts only exact engine-owned rejection rows from procenv.Registry
  in the environment reference; recipes beside them, false classifications,
  changed policy text and copies in guides still fail.

## Acceptance and verification

Five composed HTTP/store flows pass: here execution before overflow, personal
execution after the hold, saturated here with personal execution, saturated here
with Cloud execution, and Cloud retry setup/exchange/start/replay. They use real
intake, source storage/download, sealing, claims, overflow controls, secret
release and cancellation. Capacity/catalog/version boundaries are fixture
adapters. They do not execute a native SDK step or Kubernetes Job.

No native acceptance matrix cell was newly unskipped. Surface and credential
policy/registration acceptance selections pass. No archived test was newly
ported; the added tests cover this launch feature and its integration findings.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole repository `go build -p 4 ./...` and focused vet on all changed packages.
- Full short capability, client, wire, CLI-surface, source, user/project config,
  API-spec/reference and doccheck suites.
- CLI burst/source/attachment/enrollment tests; controller burst/source/secrets,
  enrollment and bootstrap exchange; store blob/source/authentication/application
  retry selections; host secret registration and native SDK masking streams.
- Final credential route declarations, serving registrations and mutation
  coverage; surface and credential-policy acceptance selections.
- Race checks on CLI/source/session paths, composed controller/store burst and
  bootstrap paths, source setup properties and source-attempt transition fences.
- Three-second fuzz campaigns: safe routing (4,620 executions), source
  authorization before bytes (6,720), pre-attempt source scope (42), and bootstrap
  source scope (32, including thirty Node/Plan/Eval seed cases).
- Doccheck, commentcheck against main (2,665 files), generated command-reference
  and mirror/symbol/changelog checks, and diff whitespace checks.

The bootstrap exchange fixture explicitly used an empty repository scope,
meaning no repositories. It now uses nil/null, meaning unrestricted, matching
the existing launcher fixture contract. The credential policy fixture now
supplies a real source-bundle target shape. No authorization check was weakened
for either fixture.

Details and reproducible selections are in
[burst acceptance](W9-BURST-ACCEPTANCE.md) and
[personal enrollment](W9-PERSONAL-CONTINUATION.md).

## Contract changes made

- C1/C2: pre-attempt sealed-source reads; source-only bootstrap downloads on the
  existing route; read/write projection by authorized operation; canonical data
  errors; fresh node data scope and pending authentication attempt semantics.
- C2/C3: RequestEvidence.ResultAttempt carries the decoded transition source
  ordinal for exact-result proof and replay. It grants no execution attempt.
- C2/C3: ephemeral independent-session runner enrollment transport. The new
  public client constructor and CLI flag are reflected in their references.
- C3 protocol/source: live bundle authorization before workspace/bytes and lazy
  credential release when bundled acquisition actually needs remote Git.

These are recorded in the four changed contract files. No schema, public wire
field, bootstrap grant, controller owner or persistent secret store was added.
Lead-owned handoffs and amendment files are untouched.

## Remaining work and native integration

- The lead must run the configured CLI through a real here host, registered
  personal runner and Cloud Job, including SDK execution, streamed/masked logs,
  Ctrl-C, artifact retrieval and parallel saturation. These tests prove the
  transport/store/source contracts, not that native composed execution.
- DESIGN retained: requester secret values live in one Store process. Intake,
  plan validation and release require that process. A different replica or
  restart fails unavailable. Multi-process Cloud needs verified affinity or an
  unpersisted relay; raw values must never enter SQL or the source bundle.
- Here placement uses current admission observations, not an atomic admission
  reservation across observation and self-claim. Native concurrency QA must
  verify prompt overflow under competing local claimants.
- Retained C1 retry-selection finding: TestReviewRetryModeIsPartOfCommittedReplay
  still fails because a changed retry mode reuses the old execution with
  replayed=true. This is the earlier source-seal/execution-selection design
  handoff, separate from the attempt identity fixed here.
- Earlier owner metadata and installed launcher composition probes now pass on
  this branch. Those passes do not replace native Cloud execution evidence.
- Other inherited integration requests remain in
  [the earlier recheck](W9-INTEGRATION-RECHECK.md). Only the explicitly rerun
  owner-metadata and launcher-composition probes above are newly closed here.

Mechanisms added are the ephemeral session transport, the before-intake error
mark, the narrow source-only bootstrap binding, and the transition source proof.
Each reuses the existing owner rather than adding a service. A member-bearer
conversion would violate independent consent; blanket bootstrap data access
would widen authority; using the previous attempt as active identity breaks
source setup. Reverting the added entrypoints and recorded bindings reverses
these additions. Doccheck's exact registry-row recognition resolves competing
checks without permitting dead recipes.

Friction: sandbox sockets prevent native services. The installed standards tool
reports source lag. No permission or operational binary was requested.

## Size and deletion

This session replaces 67 production Go lines and adds 301, net +234 across the
integration slice. No whole legacy implementation was replaced by this new
feature; earlier legacy cuts remain. W9-owned production is +132/-24 since the
starting HEAD, net +108. W9-owned production versus main is +18,186/-23,823,
net -5,637, within the -2,900 budget. Counts exclude tests, testdata, acceptance
and contract declaration files.

The only changed private source signature is cloneCommit; its submodule caller
was migrated in internal/source/submodules.go. No removed API has an unmigrated
caller.

## Shared files touched

- `.apidiff/pkg_controller_client.txt`
- `CHANGELOG.md`
- `api/openapi.yaml`
- `design/rewrite/contracts/c1-data.md`
- `design/rewrite/contracts/c2-credentials.md`
- `design/rewrite/contracts/c3-controller-wire.md`
- `design/rewrite/contracts/c3-protocol-sdk.md`
- `internal/capability/authentication.go`
- `internal/capability/authentication_types.go`
- `internal/capability/data.go`
- `internal/capability/policy.go`
- `internal/capability/routes.go`
- `internal/doccheck/bannedtokens.go`
- `internal/doccheck/bannedtokens_test.go`
- `internal/source/authorization_test.go`
- `internal/source/bundle.go`
- `internal/source/checkout.go`
- `internal/source/submodules.go`
- `internal/controller/burst_acceptance_test.go`
- `internal/controller/burst_overflow_test.go`
- `internal/controller/credential_route_expectations_test.go`
- `internal/controller/data_authority.go`
- `internal/controller/engine_amendment_test.go`
- `internal/controller/engine_routes.go`
- `internal/controller/personal_enrollment_transport_test.go`
- `internal/controller/policy_router.go`
- `pkg/docs/changelog.md`
- `pkg/docs/mirror/_sidebar.json`
- `pkg/docs/mirror/api-reference.md`
- `pkg/docs/mirror/cli-runners.md`
- `pkg/docs/mirror/data-uploads.md`
- `pkg/docs/mirror/local-execution.md`
- `pkg/docs/mirror/migrations/_unreleased.md`
- `pkg/docs/mirror/storage.md`
- `internal/store/authentication_binding.go`
- `internal/store/authority_binding.go`
- `internal/store/authority_issue.go`
- `internal/store/blob_authority.go`
- `internal/store/blob_authority_projection.go`
- `internal/store/blob_bootstrap_source_test.go`
- `internal/store/blob_data_access.go`
- `internal/store/blob_persistence.go`
- `internal/store/blob_read_authority.go`
- `internal/store/blob_source_setup_test.go`
- `internal/store/launcher_snapshot_test.go`
- `internal/store/node_state.go`
- `tests/acceptance/credential_policy_test.go`

All checks joined. Worker scratch was removed. The shared build cache is retained.

STATUS: CONTINUE
