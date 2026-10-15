# W9 continuation handoff

W9 remains incomplete. This continuation starts at `9c52db74d`. The worktree
builds. No git writes or Sparkwing executables ran. The lead owns commits,
review, pipelines and native acceptance.

## Completed before this continuation

- Final grammar validation runs before side effects. Help, completion and
  command discovery derive structure from `clisurface.Registry`.
- Local run, trigger, plan and lint parsing preserves arguments after `--`,
  explicit output and pause controls. Removed vocabulary is rejected.
- Profiles are machine-only and redact credentials. Failed revocation retains
  the connection. Project credentials and Cloud dispatch are deleted.
- The authenticated engine transport preserves typed results, structured
  errors, API compatibility and route-owned retry policies.
- Cancellation, status, run listing, prune, logs/search, annotations and
  approvals use public controller interfaces for local and remote connections.
  Numeric selectors reach the controller unchanged. Follow preserves delayed
  matches, drains pages and waits for persisted final logs. Prune completes
  selection before deleting. No CLI SQLite handle remains in these adapters.
- Dashboard commands use W8 lifecycle/unit interfaces. The separate supervisor,
  PID records, locks, platform implementations and restart command are deleted.
- Upgrade uses W8 engine contracts and staged verification. Native npm command
  policy is shared with depcache and TypeScript installation. Engine staging,
  software policy and process-environment implementations still need integration.

## Implemented in this continuation

### Statistics and sizing

Run statistics use `RunReader` and consume every matching root-run page. Filters
apply before pagination. The former 500-row cap and direct SQLite reads are
removed. Output separates successful, failed, cancelled and active runs.
Success percentage counts all terminal runs. Timed runs use an integer mean
that cannot overflow its sum and nearest-rank P95. A failed selection emits no
partial totals. Plain emits pipeline names.

Sizing uses the new W9 `SizingManager` interface on existing `GET /api/v1/sizing`.
The public view keeps separate repository/pipeline identity, job rows, CPU and
memory distributions, sustained charge, waits, memo exclusions, floors, pins,
previous measurements, controller-selected source and drift. The CLI does not
recompute admission source or decode legacy profile-key formats. It consumes all
pages and rejects repeated identities and cursors before printing.

The CLI validates reset selection and renders `SizingResetSummary` through the
interface. C2 has no authorized reset route. The production adapter returns
`policy_missing` without a request. Real reset, floor clearing and pin retention
remain W1/W2 integration work. Fake summary tests are not evidence of mutation.

### Pools

Pool commands now have a dispatch path and typed `PoolManager`. Lists retain
runner reservations and full rules. Allow preserves repository/branch selectors,
GitHub source, route bit, server rule ID and replay marker. Disallow preserves
whether a rule was deleted or already absent. Mutations use existing team-admin
routes. An allow request has a stable header key across transport retries.
Typed repository strings never substitute for verified repository authority.

### Credentials

Team and deployment commands use `TokenManager` with separate route namespaces.
`operator tokens` no longer calls the team handler. Member/runner kinds stay on
team routes and service/deployment kinds stay on deployment routes. The issuer
still owns grants, membership, metering and root provisioning.

Creation and rotation preserve typed metadata and return the new bearer once in
plain output or in their JSON result. Metadata reads never contain a bearer.
Exact scope names survive all formats, without an admin-to-wildcard expansion.
Revocation requires a confirmed result. Mutations retain structured refusals and
use stable header keys and bodies across retries. Omitted TTL remains absent,
explicit zero survives, and fractional or negative seconds fail before connection
resolution instead of being truncated. No maximum lifetime was added.

W9-owned wire additions are documented in `contracts/c3-controller-wire.md`.
W2 must serve those shapes on the existing routes. No C2 contract was edited.

## Deleted

This continuation removes 1,151 tracked Go lines, counted as gross deletions in
`git diff --numstat -- '*.go'`, not net reduction. Deleted files are
`cmd/sparkwing/jobs_verbs.go` (231), `runs_capacity.go` (296) and
`runs_capacity_test.go` (304). Shared display helpers moved to
`display_helpers.go`; their moved lines are part of the gross count.

The old statistics/sizing parsers, local store reads/resets, profile grouping,
source derivation and legacy-key matching are gone. Token pflag handlers and
formatting are replaced. Unused `Client.ListTokens`, `Client.RotateToken` and
`RotatedToken` are deleted. `CreateToken`, `LookupToken`, `RevokeToken` and
`TokenInfo` still have runner/profile callers and remain until those migrate.
The client API snapshot was regenerated, including earlier W9 removals.

## Acceptance and tests

New supplemental acceptance test
`TestSurfaceManagementTransportsKeepAuthorityNamespaces` passes for api.sock and
remote URL fixtures. It checks pool details, distinct token namespaces and
sizing fields through the real HTTP client with a fake transport. Earlier W9
transport and grammar slices also pass. Full product surface cells remain
skipped because the runtime driver and owner implementations are incomplete.
No native or launch-QA cell was activated on fake evidence.

Ported token tests retain exact scope arrays, metadata columns, revocation
visibility and empty NDJSON behavior. Sizing tests retain CPU/memory/wait
measurements, job rows, source, drift, floors and reset-summary counts. Legacy
profile-key migration tests were deleted with the removed compatibility path.
Actual reset mutation tests await the missing W1/W2 contract.

New regressions cover statistics beyond one page, atomic failure, cancellation
counts, nearest-rank P95, invalid modes, pool authorization failure and replay,
credential route separation, explicit TTL presence, exact retry bodies/keys,
malformed metadata and plain/JSON mutation output. T1's parked-test inventory
assigns no source test directly to W9.

All Go commands use GOWORK=off and GOFLAGS=-buildvcs=false, with GOCACHE,
GOTMPDIR and TMPDIR under `/tmp/sw-astra-W9/` and at most `-p 4`.

Passed after code changes:

- Whole-repository `go build -p 4 ./...`.
- Vet for cmd/sparkwing, clisurface, controllerwire, controller/client and
  tests/acceptance.
- Focused cmd/client tests matching `TestRunStatistics|TestPoolManagement|
  TestPools|TestSizing|TestToken|TestCredential|TestRenderTokens|TestRunListing|
  TestRunPrun|TestRunLogs|TestRunReview|TestRunStatus|TestEngine|
  TestCommandReferenceMatchesRegistry`.
- Full clisurface, controllerwire and tests/acceptance package tests. The last
  package still has skipped product scenarios.
- Embedded-doc and changelog file drift tests, `sync-docs.sh --check`, the command
  reference drift test, commentcheck against HEAD and `git diff --check`.

The full pkg/docs test run could not complete because
`TestWebClient_Versions_HappyPath` uses httptest.NewServer and the sandbox refuses
TCP listeners. The two file drift tests pass separately. Full legacy cmd/client
suites, real HTTP/socket, native Windows, daemon/service and engine integrations
were not run. Prior sessions recorded old-schema orchestrator failures.

## Work still required

1. Retry selection needs a W1/W2 contract for failed versus all jobs. Migrate its
   old CLI/client code after that decision. Finish profile/runner credential
   callers, restricted secrets, queue, cache warm and other management commands.
2. Wire W3 hosting and W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition are unfinished.
   Detached execution still launches the removed consumer entrypoint. Move it
   to the local controller queue, then delete the consumer/process helpers.
3. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor, schedule selectors/pause/resume and __tick.
4. Replace profile.Load/Save and connection selection with W6 final machine
   parsing/persistence and W2 credential metadata.
5. Finish older guides, scripts, examples and runtime tests that still contain
   removed vocabulary and schemas. Full auth-guide migration remains open.
6. Run every native surface cell and launch QA after owner implementations land.

## Contract changes needed in other areas

- W1/W2: retry failed/all selection and an authorized sizing-reset operation that
  clears learned samples/floors while preserving explicit pins.
- W2: serve C3 public run/query/deletion/log/review, sizing, pool and token
  projections, including token mutation replay and confirmed revocation. Implement
  apierr.DecodeInner. Client error tests still require decoder fakes.
- W2: a single ordered mixed trigger/job claim route, profile scope enrollment,
  restricted secret writes, run-scoped annotations and ordinary-member annotation
  authorization. These are gaps, not approved product restrictions.
- W7: persisted tagged logs, cursor continuity and final completeness.
- W8: runtime/unit construction, canonical log source, access-policy and
  install-force fields, and effective endpoints. No W8 contract was changed.

## Shared files and friction

Shared files touched are `tests/acceptance/surface_contract_test.go`,
`.apidiff/pkg_controller_client.txt`, `CHANGELOG.md`,
`design/rewrite/contracts/c3-controller-wire.md`, this handoff,
`pkg/docs/changelog.md` and generated mirrors for cli-operator, cli-pools,
cli-runs, cli-tokens and observability. No production caller in another owner's
package needed editing. `cmd/sparkwing/main.go` routes the changed W9 handlers.

Command docs were generated through their existing Go renderer in a temporary
test, because the swarm forbids running Sparkwing executables. The temporary test
was removed. The socket restriction prevented the full docs suite. No pipelines,
installed binaries, services, git metadata or external files were changed.
Scratch is deleted before the final report. Git identity remains Korey Gambill
and core.worktree is unset. The lead still needs to review this uncommitted work.
