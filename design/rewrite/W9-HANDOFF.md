# W9 continuation handoff

W9 remains incomplete. This continuation starts at `297a12166`. Earlier work
is committed. This continuation is uncommitted for the lead to review and
commit. No Sparkwing executable, service, repository Git metadata, external file
or harness memory was changed. The assigned environment was reused.

## Earlier work retained

Earlier commits supply final grammar, help/completion/discovery, local run and
trigger parsing, machine-only connection profiles, authenticated engine
transport, public run observations, listing/prune/log/review adapters,
statistics, sizing reads, pools, separate team/deployment token managers,
dashboard lifecycle adapters, staged upgrade verification, typed secrets,
runner observations and confirmed member-token revocation for profile removal.
Do not redo these. Their native owner integrations remain unfinished.

## This continuation

### Schedule client and CLI

`ScheduleManager` uses the existing final C2 schedule routes through the same
profile/api.sock connection transport. It implements list, selected detail,
pause, resume, override, reset, immediate run and selected deletion. Remote
repository installation and withdrawal now use `ScheduleRepositoryManager` on
`/api/v1/schedules/repos`. The CLI no longer sends schedule requests to `/crons`.

The final `schedules` dispatcher accepts repeatable/comma-list `--schedule`
selectors. It resolves every selector before the first mutation and deduplicates
canonical IDs. A selection failure mutates nothing. Subsequent mutations are
sequential, without cross-schedule atomicity. Local pinning has the same
selection preflight but still uses the older local runtime after selection.

Reads retain separate declarations, overrides, effective cadence, binary pin,
timer health and fire history. JSON listing emits compact selection metadata,
excluding arguments/history/binary paths. Selected JSON retains complete safe
controller detail. Plain emits schedule IDs or the new run ID. Pretty detail
retains declared/override/effective values and reports stale overrides. Upcoming
instants come from the controller, not a calculation against the CLI clock.
Timer reports preserve failure status after rendering the diagnosis.

Overrides preserve unset, explicit empty zone, zero catch-up, empty values and
an empty argument map. `--arg=` clears the argument map. Duplicate names,
invalid cadence/time zones/durations, mixed report modes and conflicting scopes
fail before connection resolution. `ScheduleOverride.Validate` is shared by
CLI and HTTP callers. The client requires confirmation of pause, reset and
supplied override fields. It validates observations and canonical aliases.
Immediate run retains one header idempotency key across retries and returns
both the canonical run ID and replay result.

The installer recognizes final `--all-repos`, `--schedule` and `--unpinned`
spellings. Its local compiler/arming/timer and pin paths remain legacy runtime
integration. `__tick` still opens the old store path. These are unfinished,
not accepted proof of the sole-controller ownership rule.

### Wire ownership and callers

Schedule response definitions moved from `internal/api/crons.go` to
`internal/controllerwire/schedules.go`. Callers migrated directly. No aliases
or forwarding shims were added. The redundant run envelope was consolidated
into `ScheduleRun`. Only the W9 C3 contract was extended. No C2 route or
permission alternative changed.

The older `client.Client` cron API remains because W2's legacy controller tests
and the legacy client integration suite still call it. Production CLI callers
use the new typed managers. Delete those old APIs with their remaining callers
when W2's final scheduler routes are integrated.

Vet also found two stale `NodeLogs.Grep` calls left by an earlier W9 API cut.
`pkg/controller/log_segments_internal_test.go` now exercises its existing HTTP
endpoint directly, keeping line-number and match-limit assertions. No removed
production API was restored. The test compiles; its socket-backed execution is
left to the lead.

### Documentation

Replaced the 532-line old crons guide with `docs/schedules.md`, updated links and
sidebar, and synced embedded copies. The new guide covers implemented client
behavior and puts unfinished installation/timer intent in a design region.
The C3 notes document request/response shapes, field presence and real server
obligations. The changelog and client API snapshot are updated. Unrelated stale
API snapshots produced by the generator were restored to their original bytes.

## Deleted

This continuation removes **2,225 tracked Go lines gross** against its starting
HEAD (`git diff --numstat -- '*.go'`). That count includes moved definitions and
replaced tests, not a net-reduction claim. Entire removed Go files:

- `cmd/sparkwing/crons_view.go`: 472 lines.
- `internal/api/crons.go`: 149 lines, moved into the wire owner.

The rest removes replaced local/remote schedule handlers, the old dispatcher,
dead time-formatting helpers and superseded tests. Installer/tick/launcher
regressions remain where their implementation is still active. Legacy
catch-up-clamping CLI assertions were removed under the frozen refusal policy.
The scheduler's old clamping implementation remains an integration obligation.

## Acceptance and tests

`TestSurfaceSchedulesTransport` passes for api.sock and remote URL fixtures
through the real authenticated HTTP transport. It covers selection escaping,
explicit zero history, pause/resume, empty/zero override presence, reset,
idempotent run results and already-absent deletion. This supplements the earlier
surface grammar, run, log, sizing, pool, credential, secret and runner cells.

Native product cells remain skipped. No fake-backed cell was relabelled native.
T1's parked source inventory assigns no original test directly to W9.

Ported schedule UI/transport regressions cover NDJSON/plain outputs,
declaration-versus-override detail, pin/override drift metadata, health failure
status, authoritative upcoming instants, selector aliases, pause/resume,
argument replacement/reset and immediate-run receipts. Added malformed-response,
selection-before-mutation, alias deduplication, exact zero/empty input and
idempotency retry cases. Persistence, authority and scheduler timing are not
proved by these fakes.

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false`, `GOCACHE` under
`/tmp/sw-astra-W9/cache`, and `GOTMPDIR`/`TMPDIR` under `/tmp/sw-astra-W9/tmp`.
Recreate those children to repeat the commands. Tests use `-p 4` at most.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` for cmd/sparkwing, clisurface, controllerwire, internal/crons,
  controller/client, controller, localws and tests/acceptance.
- Full clisurface, controllerwire and tests/acceptance package suites, retaining
  the skipped native product scenarios.
- CLI/client tests selected by `^TestScheduleManagement|^TestScheduleManager|
  ^TestScheduleRepository|^TestCommandReferenceMatchesRegistry|
  ^TestSecretManagement|^TestRunnerListing|^TestRunnerReader|^TestToken|
  ^TestPool|^TestRunStatistics|^TestSizing|^TestEngine|^TestLogReader`.
- `TestPkgDocsContentMatchesDocsRoot`, `TestEmbeddedChangelogMatchesRoot`,
  `sync-docs.sh --check`, commentcheck, sleepcheck and `git diff --check`.

Attempted and failed:

- Full `internal/crons` suite. Its legacy arming paths fail the final schema's
  `NOT NULL cron_schedules.repo_key` constraint. Git-backed fixture tests also
  fail because inherited commit signing needs the read-only machine GPG home.
  No global Git setting was changed. This suite is not a pass.

The lead owns pipelines, full legacy package suites, socket-backed controller
and localws tests, native Linux/macOS/Windows surface cells and launch QA. The
updated controller grep test was compile/vet checked, not socket executed.

## Work still required

1. Compose W3 hosting with W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition remain unfinished.
   Detached execution still starts the removed consumer entrypoint. Move it to
   the local-controller queue, then delete consumer/process helpers.
2. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor, schedule local install/pin/uninstall and
   `__tick`. Delete the direct-store schedule paths after this composition.
3. Implement W2's final schedule projections and transaction adapters, including
   aliases, requested history/upcoming counts, masked arguments, empty/zero
   override presence, no clamping and idempotent immediate launch. Migrate W1's
   legacy scheduler repository identity writes to the final schema. Then port
   the remaining legacy cron integration tests and delete the old Client API.
4. Migrate runner enrollment/service config, remaining runner token callers,
   queue, cache warm, legacy client APIs and other management paths.
5. Replace profile.Load/Save and connection selection with W6's final machine
   parsing/persistence. Profile scope enrollment remains a W2 gap.
6. Finish the other guides, scripts, examples and runtime tests still using
   removed vocabulary/schemas. Auth.md still needs its remaining migration.
7. Execute every native surface cell and launch QA after owner implementations.

## Contract changes needed in other areas

Existing gaps remain:

- W1/W2: retry failed/all job selection and an authorized sizing-reset operation
  that clears learned samples/floors while retaining explicit pins.
- W2: ordered mixed trigger/job claims, profile scope enrollment, run-scoped
  annotations and ordinary-member annotation authorization.
- W8: runtime/unit construction, canonical logs, access-policy/install-force
  fields and effective endpoints. ManagedUnitManager has no schedule
  installation/uninstallation method, and LocalRuntime exposes only Run.
  The stable tick has no declared way to request one scheduler pass from the
  sole local controller. W8 must supply those seams before W9 can replace
  local timer/pin/tick plumbing. No W8 contract was changed here.

Real integration also needs W2's other C3 projections and apierr.DecodeInner,
W1/W2 encrypted/restricted-secret persistence and enforcement, W7 persisted
log/cursor/final completeness and W3/W6/W8 host composition. No new product
restriction, environment variable, service or timeout was introduced. No new
human decision is requested.

## Shared files and friction

Minimal production callers outside W9 changed only to import the moved wire
models:

- `internal/crons/view.go`
- `pkg/controller/crons.go`
- `pkg/localws/crons.go`

Shared tests migrated for those wire models:
`pkg/controller/crons_test.go`, `pkg/controller/crons_team_test.go`.
The earlier removed API's caller fix is
`pkg/controller/log_segments_internal_test.go`.
The replaced shared definition is `internal/api/crons.go` (deleted).

Other shared changes are tests/acceptance/schedules_contract_test.go, README.md
(schedule-guide link), CHANGELOG.md, .apidiff/pkg_controller_client.txt, the C3
contract, this handoff, pkg/docs/changelog.md and generated doc mirrors. All
other production changes are in W9-owned packages.

Friction: the legacy scheduler does not satisfy the final schema, Git fixture
signing cannot access the machine GPG agent, sockets/native processes remain
outside the worker verification path, and the final runtime contracts lack the
schedule install/tick seams above. Git identity remains Korey Gambill and
core.worktree is unset. Scratch is deleted before the final report.
