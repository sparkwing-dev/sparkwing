# Local controller grid verification

The baseline CLI was built from the supplied worktree at `1f4998a3e`.
Each runtime used a private HOME and SPARKWING_HOME under
`/tmp/sw-codex-grid-local/`. Fixtures came from the named grid evidence and used
a local SDK replacement pointing at this worktree. No installed CLI, real store,
cluster or cloud service was used.

The baseline build command was:

```sh
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C "$WORKTREE" build -o /tmp/sw-codex-grid-local/bin/sparkwing ./cmd/sparkwing
```

The build variable WORKTREE names the supplied checkout. Runtime environments
retained the shared GOPATH, GOMODCACHE and ASDF_DATA_DIR required by the harness.
An explicit Go version was needed under the private HOME. Tests used the same Go settings
and a private TMPDIR. The final CLI built at `893fc28e6` passed a private `run hello --sw-no-update`
smoke test (`run-d0ba77f039636d455e2bac21`). Its SHA-256 was
`a77d5beb5afdd45405da1e8df79028b46e9ab8715b549b5abd8d2a0439c76c82`.
Both final private controllers were stopped, no candidate or polling process
remained, and `/tmp/sw-codex-grid-local/` was removed after preserving this evidence.

## Cell results

| Cell | Result | Evidence |
| --- | --- | --- |
| R06C02 | Fixed | Baseline diamond run `run-78af2eac5bcb6a8fe833ff92` had successful C and cancelled D with attempt 0 and no explanation. Rebuilt `runs status --run ID -o json` reports `upstream_failed: blocked by "b" (failed)` for D. |
| R06C08 | Fixed | A visible third waiter originally returned `found=false`, `not in local admission`, previous position 2. After `queue priority --run ID --set front`, fixed run `run-510c5ed9424c8ac58dab111d` had position 1. All three runs succeeded. Stored step start times prove first, third, second order. A node-only holder also reports `holding=true`. |
| R06C15 | Fixed | With a running 180-second holder and a two-second queue timeout, baseline `run-10c012999bb0be0823691d5f` failed before attempt 1 without a status explanation. Rebuilt status of that run reports `queue_timeout: timed out waiting to start`. |
| R06C16 | Fixed | Five `stats-hello` runs succeeded. Baseline stats omitted P50. Fixed `runs stats --pipeline stats-hello -o json` over the same runs reports `p50_duration_ns=11239000000`. |
| R06C17 | Stall fixed; separate owner-loss failure remains | Baseline 200-job run `run-cf7445f62671044f3892abe0` timed out on status. Rebuilt run `run-ea644d2865b019ff65b14d5e` completed all 200 jobs successfully in 14m11.829s, using the existing private `admission.mode: classic` and `budget: 4,ignore-external`. Its 183 status samples took at most 6.283s. Default-admission companion `run-e5fc92901fd0c068c3032df5` remained responsive (182 status samples, maximum 4.341s) but ended `owner execution lost` after 122 successful and 78 cancelled jobs. |
| R07C05 | Already fixed | Baseline local approve succeeded as `local:1000`. Run `run-7356187a381f4bf28b7a13f2` and its downstream job succeeded. Fixed verification run `run-1ccbf04d9307ae46eab5e467` also succeeded. |
| R07C06 | Already fixed | Baseline denial succeeded as `local:1000`. Run `run-8900c04a442cb6a48fe711f6` failed and its downstream job was cancelled. Fixed run `run-668a3a9c0617671791f6ee5d` has the same terminal outcomes. |
| R07C07 | Fixed | A ten-second approval took 41.038 seconds on the baseline. Fixed run `run-d848dec4402273f8270b59eb` requested approval at `21:09:13.290Z` and resolved at `21:09:23.903Z`, 10.613 seconds later. |
| R07C13 | Fixed | Resume authorization was already fixed, but baseline `run-482211e3dee04c0807d64986` never persisted a pause. Its daemon logged `debug pause requires an authority-bearing boundary adapter`. Fixed `run-da5523e58b2a9a5341bda658` exposed the before-produce pause, resumed and succeeded. |
| R07C14 | Fixed | Isolated baseline daemon answered but had the same missing pause adapter. Fixed run `run-5d55f713883f9e6df3297dad` exposed `pause-on-failure`, resumed and ended failed with the original exit error. |
| R07C17 | Authorization already fixed; verified with active pause | Both resume calls succeeded on the baseline without a pause. Both calls also succeeded for the real persisted pause in `run-da5523e58b2a9a5341bda658`, which then succeeded. Tests preserve the execution fence on replay. |
| R07C19 | Fixed | Baseline cancellation left the gate in pending approvals. Fixed `run-816675df579ce052dbbef026` ended cancelled, disappeared from the unfiltered pending list and retained per-run request history. |
| R08C18 | Already fixed | Fresh terminal runs reached the existing 15-minute log-upload pin rather than a forbidden error. After stopping the private controller and aging only synthetic `log_slots.created_at` rows by one hour, `runs prune --older-than 1ns -o json` succeeded and removed all three terminal IDs from the following list. Immediate deletion through the existing pin was not claimed. |
| R09C15 | Fixed | Baseline `cache warm` followed by successful verbose `run hello` had no hit line. Fixed run `run-967f8304aff1c1bd8d18b77c` succeeded and emitted `Cache hit: pipeline binary`. Cold compilation does not emit a hit. |
| R09C16 | Already fixed | Memo run `run-56e5d17cbe2281a23e3ae23f` executed GRID_START and GRID_END and succeeded without the digest-read refusal. Existing machine-local memo publication remains disabled, so this does not claim selective local memo reuse. |
| R10C01 | Already fixed | Synthetic `secrets set --secret FOO` succeeded. Run `run-cf983dddc1f4a8e11dff39e3` succeeded and printed `GRID_SECRET ***`. |
| R10C08 | Already fixed | `secrets set --secret ABC` accepted synthetic value `xyz` and warned that values shorter than four characters cannot be masked. This matches the documented policy. |
| R10C11 | Fixed | Baseline missing BAR advice included `--profile <profile>` and lacked set syntax. Fixed run `run-99e867901256c0a723551368` names BAR and `sparkwing secrets set --secret BAR --file <path>`, without remote-profile advice. |

## Large-plan runtime evidence

The baseline readiness failure occurred before the normal 45-second API request.
The daemon's one-second readiness handshake pinged the busy single SQLite writer.
An isolated held-writer regression reproduced that block. A CPU profile also
measured roughly 77,200 SQL operations for 200 ready/reconcile pairs; batching and
targeted reads remove the repeated work without a new pool or cache.

Both final 200-job runs used the complete functional fix through `efaeb4499`.
Later commits changed documentation, help text and generated schema only. Early
Python polling used a 12-second wrapper deadline, which cut off a legitimate API
request. Final polling allowed 60 seconds so the CLI owned its existing 45-second
request deadline. No product deadline was raised. Neither final run had another
readiness-handshake failure or status API request deadline failure. The default
companion did lose its owner; successful status polling is not evidence that all
background API requests succeeded.

## Regression evidence

Each regression was executed against old code before its fix, directly or with
a Go source overlay. Failing assertions covered missing status explanations,
missing queue membership, lost node-participant priority, missing P50, late
approval expiration, cancelled pending approvals, absent cache-hit diagnostics,
remote advice for local secrets, missing debug persistence and dispatch after
cancellation during a pause.

The scaling tests also failed on 200 placement queries per snapshot, 200
redundant whole-plan reads during release, and 400 row/retry queries for one
200-job snapshot. The readiness regression timed out after five seconds on old
code while blocked in `PingContext` behind a held SQLite writer. Their fixed expectations are one
placement query per snapshot and zero repeated plan reads during ordinary
release. The placement fixture mixes matching and nonmatching labels and
includes a 164 KB sealed argument payload. Targeted owner-node reads fell from
200 row reads to one. Child authentication resolves a requested identity directly
instead of reading every sibling. Its HTTP regression failed on old code when
an unrelated sibling had invalid persisted execution JSON.

Focused checks passed for admission, priority, stats, approval maintenance,
approval cancellation, materialization, secret preflight, unstarted status,
owner placement and funding, canonical claim predicates, owner scheduling,
dispatch, readiness behind a held writer, and nine debug combinations
(before/after/failure with manual/timeout/cancellation release). Independent correctness reviews covered
all implementation commits and found no remaining blockers. The final CLI and API
documentation drift checks and `go build -o /dev/null ./cmd/...` also passed.

The lead owns the full gate and its test-output file, as requested. These
focused results do not substitute for that gate. PostgreSQL, Kubernetes and
cloud execution were not exercised in this lane.

The scaling regressions are directly runnable with:

```sh
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C "$WORKTREE" test ./internal/store ./internal/nodehost \
  ./internal/controller ./internal/wingd/localcontroller \
  -run 'Test(ReadSnapshotLoadsNodeRowsTogether|OwnerSnapshotPlacementWorkDoesNotMultiplyByJobCount|OwnerNodeReadDoesNotLoadUnrelatedJobs|OwnerReleaseDoesNotReadThePlanAgainForEachJob|LocalHostChildAuthorityDoesNotReadUnrelatedJobs|StateReadinessDoesNotWaitForStoreWriter)$' -count=1
```

## Mechanisms and scope

The debug change supplies the missing owner-authorized store adapter and uses
the existing state follower. The existing public pause command cannot supply
that boundary because it requires run-control presentation and rejects terminal
jobs, including the jobs paused after failure. Undoing this choice removes the
adapter, owner pause writer and native binding together.

The scaling changes batch the existing placement predicate and reuse immutable
run facts while releasing ordinary jobs. Debug release still refreshes
cancellation state. There are no new configuration fields, environment
variables, database schema changes, compatibility shims or customer-visible limits. A
separate SQLite reader pool was investigated with source overlays and concurrency
tests, but is not part of the committed fix. Readiness uses the established
controller lifecycle instead of blocking its handshake on a database ping. API
requests continue to surface database errors.

Remaining observations for the lead:

- The default-admission 200-job companion lost its owner after 122 successes.
  Its local root, owner trigger and child credentials had no expiry or revocation;
  configured local execution timeout was zero. All remaining admission
  connections closed at `22:30:22Z`, before the first credential refusal at
  `22:30:23Z`. The host logged `context deadline exceeded` at `22:31:22Z`.
  The owner reaper recorded `agent_lost` at `22:33:17.333Z`, after the last renewal
  at approximately `22:30:17Z`. Credential errors are a consequence. The initial
  deadline producer remains unproved. This is a separate runtime failure from
  the readiness stall, and the default 200-job run is not claimed successful.
- A required-secret refusal can leave an admitted run pending in the synthetic
  fixture. Cancellation then reports a request without reaching a terminal
  state. This lane changed the local repair message only.

## Follow-up verification

This session resumed at `46a35e28b`. The original cell results above remain the
record for completed work. The fresh baseline CLI reproduced the remaining
large-plan request pressure and the missing-secret run that stayed pending.
All runtime commands used private homes under `/tmp/sw-codex-grid-local/`.

### Lead-gate failures

- `TestTeamBoundary_ListsNeverIncludeAnotherTeam` failed because its synthetic
  node was `pending` while its approval row existed. The fixture now uses
  `approval_pending`, matching the pending-list contract. All focused
  `TestTeamBoundary_` checks pass.
- The ten-second approval regression seeds state after acquiring its fixture
  binding, so it uses its test context rather than bypassing the constructor's
  context. The deadline assertion remains unchanged and passes.
- The placement cursor uses the existing `closeRowsInto` helper. Lint no longer
  reports its unchecked deferred close.
- The unused `ReadOwnerNodes` export was removed. Guardcheck reports
  `every guard matches its allow-list`; the allowance was not enlarged.

### Runtime and negative controls

The large-plan baseline was built at `46a35e28b`. Run
`run-d758e3f4161f60eec1290c34` reproduced HTTP request deadline failures during
200-job startup. Its daemon recorded eleven authentication HTTP 500 responses,
with maximum authentication latency 57.152 seconds. A run-detail request was
cancelled after 45.043 seconds. The CLI eventually obtained running status
through retries, but that does not make the timed-out requests successful.
Cancellation of this synthetic run was requested before replacing its controller.

The remaining work was repeated placement and declaration materialization on
single-job lookups, including child authority checks and renewals. Scheduling
uses placement from whole snapshots. Single-job reads now retain the same
owner authority, relation, holder, generation and lease checks but load only the
named job. They omit placement and the whole run declaration. The separate
placement-free export and wrapper were removed. Snapshot placement still
uses the canonical shared matcher.

Negative controls executed before their changes:

| Regression | Old result | Fixed result |
| --- | --- | --- |
| `TestLocalHostChildAuthorityDoesNotReadUnrelatedJobs` | Two child lookups resolved placement four times | Zero placement resolutions |
| `TestPrivateHostNodeReadsStateWithoutPlacement` | Single-job transport resolved placement twice and returned a placement projection | Zero resolutions and no projection |
| `TestOwnerNodeReadDoesNotLoadUnrelatedJobs` | Single-job lookup materialized one whole run declaration | One named job read, zero declaration reads, zero placement queries |
| `TestOwnerHostMissingRequiredSecretFinishesUnacceptedRun` | Real-store run stayed pending after `plan_invalid` | Run failed without accepting a plan and retained the repair command |

Final CLI SHA-256: `3de70596482bb708106ea96e773273f0aac9c68214d5b5528b409257c27da989`.
Its functional source includes `77d048ae2`. The final default-admission run is
`run-8920125dd088f31f1625041c`, using the original fixture's `wide`, `n=200`,
`delay=0s` invocation with no admission overrides.

R06C17 passed: all 200 jobs finished successfully in 9m49.601s. All 93
status samples succeeded in reading the run, with maximum CLI latency
16.274 seconds. The controller recorded zero HTTP 5xx responses,
and no owner loss occurred. This closes the previous session's residual
large-plan verification observation.

The same CLI submitted missing-secret run
`run-7449e8e2bd5ac04763189667`. It finished `failed` at
`2026-10-29T23:41:32.918Z`, with zero jobs and the failure text
`sparkwing secrets set --secret BAR --file <path>`, without remote-profile
advice. Deterministic `plan_invalid` acceptance refusals use the existing
preparation completion path. Transient acceptance errors keep their prior
handling. This closes the pending-secret observation from the previous session.

### Checks and review

Focused Go checks passed for tenancy boundaries, approval maintenance, owner
placement and targeted reads, private transport and child authority, owner
preparation and scheduling, local owner renewal, and release scaling. The
new negative controls above were observed failing before their fixes. Missing
nodes and foreign, superseded, revoked and expired owners remain refused.
Snapshot placement tests still cover matching and nonmatching labels and
funding.

The final controller/store lint run reports `0 issues`. The broader follow-up
lint run over controller, store, localcontroller and nodehost also passed.

The documentation/mirror checks, changelog check, final CLI build
and `go build -o /dev/null ./cmd/...` passed. The lead still owns the full gate.
Its unrelated Helm-version and missing TypeScript dependency failures were not
changed in this lane. PostgreSQL, Kubernetes and cloud execution were not run.

Independent read-only correctness reviews covered committed snapshots through
`77d048ae2` and found no actionable findings. The final simplification removed
the duplicate API, wrapper, placement flag, unused placement context and
unused declaration read. There are no new production mechanisms, configuration
fields, schema changes, compatibility shims or customer-visible limits.

All private controllers were stopped, no owned runtime or monitor process
remained, and `/tmp/sw-codex-grid-local/` was removed after recording this evidence.
