# W1 store review corrections

The seven entries in the supplied store review have implementation and regression
coverage in this worktree. HEAD remains `8a93238d1`. No Git write occurred.

The native report was read first. It passes `pkg/store` on the previous committed
state but contains failures in `bin` and `pkg/controller`. It has not judged this
worktree. Completion remains with the lead harness.

## Implemented

1. GC checks the current maintenance lease after digest-lock waits and verifies
   the same collecting tombstone immediately before physical deletion. Final
   metadata removal also requires that tombstone. S3 uses HEAD's ETag in the
   delete's `If-Match`. Missing ETags and failed conditions preserve the fence.
   The lease-expiry regression ran red before the fix, with one physical delete
   after expiry, and passes afterward with none. Filesystem and S3 missing-byte
   tests refuse empty successful outputs and manifest reads. Generated remote
   memo/depcache cases remove output, manifest or member bytes and require errors
   without artifact access. Remote lookups now preserve verification errors.
2. Launcher preparation and rejection discover bootstrap and current unit
   capability prefixes before locking work. They prelock the complete authority
   set, lock units, then revalidate the set. A changed set rolls back and restarts
   discovery. The false comment is removed. PostgreSQL tests cover exchanged
   capability renewal and exchange during discovery.
3. Local publication collects every pending manifest in a snapshot pass. Ten
   distinct manifests ran red against the old implementation and pass with the
   batch. Existing continuously changing graph coverage remains.
4. GitHub report advisories use a separate pool limited to two connections.
   Callback and metadata work retain the operational pool. PostgreSQL tests use
   `SetMaxOpenConns(1)` and 25 reports with a `pg_blocking_pids` barrier.
5. Complete transaction bodies for tenant/operator revocation, membership,
   team deletion requests, account deletion, purge finalization, retention,
   sign-in and console mutations enter `inTx`. The audited bodies perform SQL
   mutations, reset results per attempt and publish generated IDs only after
   commit. Secret cleanup remains after commit. Console approval clones its
   selected IDs on every attempt. Sign-in keeps its existing uniqueness retry
   around the PostgreSQL conflict boundary. Nine PostgreSQL deadlock cases use
   real concurrent transactions and SQL triggers, with no sleeps.
6. Source reservation tests seed a committed storage quota row. Reservation and
   commit NOWAIT probes now assert that they selected exactly one admission or
   reservation row.
7. `lock_order.go` owns the tier table and comparator used by authority and unit
   batch locking. A generated shuffle test records actual SQL acquisitions and
   checks the resulting sequence, including a session before its capability
   parent. `STORE-DATA.md` matches the implementation.

Fresh API intake and retry requests acquire their run-cap token after their
discovery transaction rolls back. A private request-scoped grant survives
transaction retries. Idempotent replays return before policy and spend no token.
A property checks repeated policy evaluation and principal scoping.

## Verification

Commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4`, `-p 4`, and Go scratch under `/tmp/sw-astra-W1`.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Pass |
| `go vet` on store, controller, blob, blob/depcache and acceptance | Pass |
| Full SQLite `pkg/store` | Pass, 345.898s |
| Blob and blob/depcache package tests | Pass, 4.443s / 0.021s |
| Final missing-byte/S3 test selection | Pass, 0.185s |
| Controller `TestFlood*` | Pass, 1.151s |
| Launcher, acquisition-order and preparation fuzz seeds | Pass, 10.842s |
| Focused store GC, reporting, publication and SQL-scope tests | Pass, 2.287s |
| Full storage acceptance selection (`TestStorage*`, `TestDataRemoteCache*`) | Pass, 159.873s |
| Missing-byte and conditional-delete properties under `-race` | Pass, 1.044s |
| `commentcheck -base main` | Clean across 2,894 files |
| `sleepcheck -base HEAD` and `git diff --check` | Pass |

The first broad storage acceptance invocation exhausted a 120-second test
budget while running generated cases. It was rerun with a 15-minute budget.
PostgreSQL attempts named `sw_w1lead` and were refused during schema opening:
`dial tcp 127.0.0.1:5433: socket: operation not permitted`. Native PostgreSQL
tests are compiled but not claimed passing. The lead must run the new admin,
launcher, report-pool and source/GC barrier tests with the full store suite.

Existing store acceptance groups pass with the full package, including sessions,
transitions, plan seals, queue/claim matching, enrollment and launcher sync. No
acceptance cell was changed. No legacy test port
was needed for these review corrections. Previous amendment and handoff work
already committed on this branch was retained. The remaining schedule-route
integration in the native report belongs to W1b under the lead's handoff.

## Contracts, mechanisms and limits

C1 records the GC/read amendment: reuse the existing collecting timestamp,
use S3 conditional deletion, and make required missing-byte reads loud.
No schema, object generation, public method signature, setting or environment
variable was added. The remote lookup error behavior is the minimal W7 edit
needed for the missing-byte ruling. Materialization retains its explicit
cold-execution miss policy. No other area's contract change is requested.

The added mechanisms follow the lead rulings: a bounded restart of launcher
discovery, a two-connection advisory pool, and the private intake token
handshake. A shared pool cannot satisfy the one-connection reporting case.
Consuming the token before discovery would charge idempotent replays. A stale
authority set requires a fresh transaction before any work can proceed.

Rare loss remains possible if a collector pauses after the last check,
including identical S3 bytes with the same ETag. This is the accepted residual.
Missing or incomplete required bytes return errors. No stub integration remains
in the seven corrected mechanisms. Native concurrency and the external S3
service remain integration evidence for the lead.

Production Go changes under the lead's filter are **464 added, 199 deleted,
net +265** across all touched areas. Store is **+192 this session** and **-35
against main**, **4,215 over** its combined W1/W1b budget of -4,250. This session
removes redundant transaction wrappers, the previous comparator and the report
lock's old connection acquisition. It does not claim a legacy subsystem cut or
a budget waiver. The review-required fixes account for the added production
code. The inherited area overage still requires a separate reduction pass.

Shared production files outside store are `internal/blob/{s3,remote}.go` and
`pkg/controller/{flood,engine_routes,engine_lifecycle_routes}.go`. Shared tests
are `internal/blob/{missing_bytes,s3_collection,remote,remote_selector}_test.go`,
`pkg/controller/flood_retry_test.go`, and
`tests/acceptance/storage_s3_test.go`. Shared prose is `internal/blob/README.md`
and C1. Lead-owned amendments and handoffs are untouched.

Friction: socket policy prevents native PostgreSQL evidence, and the broader
storage property suite needed more than two minutes. No daemon, provider call,
release, deployment, Git write or harness memory write ran. Git config contains
no user identity or `core.worktree` override. Scratch is removed at session end.
The shared build cache is retained.

STATUS: CONTINUE
