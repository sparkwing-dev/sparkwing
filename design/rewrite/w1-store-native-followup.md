# W1 native follow-up

The previous session's seven review corrections remain in place. This session
fixes the failures in the lead's latest native report and removes two unused
legacy mechanisms. HEAD is `54b6c6c5f`; no Git write ran.

## Implementation and invariants

A transaction finishes reading a cursor before issuing another SQL query.
Completion readers now capture one database time before opening their cursor,
using it for both the due cutoff and every returned `ObservedAt`. The existing
PostgreSQL failures came from `tx.now()` issuing a clock query inside that cursor.
A strict single-connection SQLite adapter reproduces pgx's cursor constraint.
The regression failed with `conn busy` before the change and passes afterward
for the general, profile and GitHub effect readers.

Auditing the same pattern found two more instances in runner inventory and
placement liveness. Both now capture their comparison time before querying.
The generated observation property failed separately in placement and inventory
before their fixes. It varies observation age, preserves valid inventory,
refuses future inventory observations and excludes stale/future placement polls.
No direct `tx.now()` call remains inside a production row loop.

The retention deadlock fixture now inserts and locks an `identity_link` nonce,
which satisfies the final schema's nonce-kind constraint. Its real concurrent
transactions and `pg_blocking_pids` barrier are retained without sleeps. The
existing launcher, report-pool and source/GC concurrency proofs are unchanged.

## Deletion and ports

Removed the unused Argon2 token/password verifier, semaphore, counters and
configuration API. Its controller flag, validation, initialization, chart value
and rendered argument are removed with it. Authentication already uses the
final capability/session digests. The Argon2 tests retire with the verifier.
The controller's login listener-budget test retains its burst assertions and
no longer configures a nonexistent hashing budget. The chart's retired-flag
test is deleted.

Removed `ValidateRunInvocation`, `ErrSecretInputHash` and their private legacy
input decoders. No production caller remains after the sealed-intake cut.
Its malformed-classification test retires with that raw-input API. Final sealed
argument and reporting-classification properties remain. The reporting
`InvocationSecretArgsKey` constant moves into `store.go`.

Removed the unused `Operator.TriggerTeam` sweep lookup and its SQL-scope
exemption. `nullableTimeNS` and `assertRunBelongsToTeamTx`, used only by fixture
code, move into their existing test files. No fixture behavior is changed.

Under the lead's production filter, this session has **13 added, 238 deleted,
net -225 Go lines**. Store alone is **-214 this session** and **-249 against
main**, still **4,001 over** the combined -4,250 budget. The two deleted store
test files contain 180 lines; the deleted chart test contains 20 lines. No
budget waiver is claimed. The inherited overage still needs reduction and lead
review; the cursor fixes add only four net production lines across three readers.

## Verification

The final Go tree was held unchanged during the final full store run. Checks
use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4` and `-p 4`. Final verification also sets `TMPDIR` and `GOTMPDIR`
to `/tmp/sw-astra-W1`.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Pass |
| Scoped vet: store, controller, controller command and charts | Pass |
| Full SQLite `pkg/store` | Pass, 279.878s |
| Completion, observation, inventory and placement regression selection | Pass, 11.438s |
| Completion, observation and profile properties under `-race` | Pass, 7.272s |
| Store session/transition/seal/queue/claim/enrollment/launcher cells, reporting and GC selection | Pass, 5.091s |
| Controller login and completion selection | Pass, 1.043s |
| Focused controller chart render tests | Pass, 6.379s |
| Store fixture packages | Pass |
| Commentcheck against main | Clean across 2,893 files |
| Sleepcheck, docs mirror, doccheck and whitespace | Pass |
| Regenerated store API snapshot | Matches |

No acceptance cell was skipped, weakened or newly unskipped. This session ports
no legacy behavior into a new API; existing final-schema cells retain it.

The first full store run overlapped the legacy cleanup. Its compiled exemption
map still contained TriggerTeam while its source scanner read the later tree,
causing its sole failure. The final run above uses one unchanged tree and passes all store tests.

PostgreSQL execution targeted `sw_w1lead` at 127.0.0.1:5433 and was refused with
`socket: operation not permitted` before schema creation. The full controller
command tests also stop at the native composition test's listener refusal.
These are unavailable checks, not native passes. The latest lead report still
contains the prior PostgreSQL FAIL entries, so completion remains with the
harness. No native report was edited.

The whole API snapshot comparison also finds inherited controller snapshot
drift for `SourceCredentialResponse.ExtraRepositories` and private intake source
helpers. Only the store snapshot was refreshed; it includes existing store
source/environment declarations that had not reached its committed snapshot.

## Contracts, shared files and remaining work

C1 and STORE-DATA describe the cursor observation rules. The port inventory
records retirement of the unused verifier. No schema, authority interface,
retry, setting, environment variable or runtime mechanism is added. There is
no foreign contract change request or policy question from these fixes.
Lead-owned amendments and handoffs remain untouched. Existing W1 adapters,
ordered placement, Cloud metering, trusted App/scheduled intake and legacy
trigger-writer removal remain implemented; no replacement of them was needed.

Shared implementation edits are limited to caller removal in
`cmd/sparkwing-controller/main.go` and the controller chart's deployment and
values. Shared tests are `pkg/controller/login_limit_test.go` and
`charts/render_test.go`. Shared prose/generated changes are C1, the port
inventory, CHANGELOG, the unreleased migration guide, their embedded copies
and `.apidiff/pkg_store.txt`.

Real-integration evidence still needed: the lead's PostgreSQL suite and native
controller composition. The accepted rare GC overlap residual and live S3
conditional-delete evidence are unchanged from the previous report. No new
stub integration was introduced. The area budget remains open.

Friction: PostgreSQL and listener sockets are denied in this sandbox. No
candidate executable, daemon, deployment or provider mutation ran. Git config
has no user identity or core.worktree override. Session scratch is removed at
finish; `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
