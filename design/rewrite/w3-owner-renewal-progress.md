# W3 attempt fences and owner renewal

<!-- design:begin -->

W3 remains incomplete. This continuation follows
[the owner assembly report](contracts/w3-owner-assembly-continuation.md) and
[the assignment audit](contracts/w3-amendment-handoff.md#amendment-audit).
No acceptance cell is added, unskipped or marked complete.

## Merge

All five conflicted working files have resolved contents. The lead must stage
them and finish the merge because the swarm brief forbids worker Git writes.
`handoffs.md` is the integration branch's version unchanged. The test inventory
keeps both branches' port notes. `debug_test.go` and `steps_test.go` retain W3's
deadline-aware waits and common step driver. `claim_exec.go` remains deleted.

## Implementation

Invariant: a holder transition names the ordinal captured by that execution.
Start, retry and result reports now send `NodeTransition.SourceAttempt`.
Claimed-host setup failures use the award's retained ordinal. Start and retry
still consume `Applied.Attempt` before another SDK request or report. The host
never rereads an ordinal to refresh a stale report. The existing executor fake
now refuses missing or stale ordinals. Its generated property varies the initial
ordinal, retry budget and a replacement attempt. The existing SQLite executor
event, step-record and foreign-award cells exercise the final store.

`OwnerHost.Renew` is now required. Its callback carries the authenticated actor
and current `Executing` value to C1's existing `RenewUnit` contract or its wire
equivalent. The liveness-only `OwnerStore.RenewOwner` requirement is removed.
This changes only W3's interface. No W1 or W9 type changes. The four existing
acceptance fixtures pass their current renewal fake through the new callback.

Preparation reports compute through acquisition and acceptance. Native assembly
then counts SDK processes per run from startup through close and cleanup.
Overlapping processes keep compute active until the last cleanup returns.
Failed startup without a process drops its count. A partial startup retains it
until cleanup. A failed cleanup also drops its count. Waiting for remote work
without a process reports false. The owner renewal test uses synctest and checks
preparation, idle, active-process and idle renewal values. The native owner and
claimed-host fixture checks that neither returns with active compute.

Native approval opening uses the owner transition and state follower without a
separate approval adapter. It stops before memo and admission. Remote jobs with
no active debug, memo or concurrency policy need no policy callback. Active
policies still require their adapter. Local execution requires admission even
without a concurrency group. Approval SkipIf success remains blocked by the C1
edge below.

The existing native predicate test exposed a request-write race. The SDK could
consume a complete frame and log the predicate's entry before the writer
goroutine published completion. Cancellation sampled the empty notification
channel and failed the healthy session. The narrowed test failed 4 of 100 runs.
Cancellation now joins the outstanding write within the existing one-second
process termination grace. A completed write proceeds to targeted cancellation.
A blocked write still fails the session and joins the process. The existing
predicate, revocation, cleanup and blocked-writer tests are retained.

## Verification

Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, at most four
test packages, and cache/build temporary paths under `/tmp/sw-astra-W3/`.
Final checks also set TMPDIR there for test fixtures.

| Check | Result |
| --- | --- |
| Whole-repository build | Passed after the final protocol change. |
| Scoped vet | Passed after the final protocol change. |
| Common owner, claimed host, executor, protocol session, policy, state follower and service selection | Passed in 5.28 seconds. |
| Existing lifecycle, environment, recovery and credentials acceptance selections | Passed in 12.35 seconds. |
| Full source, lease and nodeproto packages | Passed. |
| Source-ordinal fuzz property | Passed 2,000 generated executions. |
| Native predicate/cleanup/blocked-writer selection, 20 repetitions | Passed in 50.18 seconds. |
| Original narrowed predicate reproduction, 100 repetitions | Passed in 5.76 seconds. |
| Focused race check | Passed in 3.24 seconds. |
| Concurrency-key and scope-label selection | Passed. |
| Commentcheck against main and sleepcheck against HEAD | Passed after the final protocol change. |
| Whitespace and conflict-marker check | Passed. |

The full nodehost suite still fails on legacy CreateRun fixtures writing removed
`args_json`, then on a forbidden HTTP listener. It terminated in 7.54 seconds.
The first broader focused command also selected the unrelated legacy remote-run
fixtures and an HTTP SDK-router test. Those failed on the same schema/socket
boundaries. Its native predicate failure was narrowed and fixed as described
above. The later selection uses exact host components and passes.

The full acceptance command hit its two-minute test limit while compiling the
Go SDK fixture in the protocol placement matrix. It is failed evidence. The
scoped lifecycle/environment/recovery/credentials check passes. No full-suite,
native HTTP/api.sock, placement, Windows or Darwin pass is claimed.

## Remaining contracts and W3 work

The earlier amendment audit remains the assignment map. This continuation closes
W3's captured-ordinal portion of the attempt-identity handoff and adds the current
compute projection requested by the W1 lifecycle/billing handoff. The established
router, eval decoder, strict raw expansion, procenv, concurrency codec, alias and
settlement implementations remain. Their remaining legacy test ports are open.

The following foreign gaps were checked against this merged tree:

- W9's `JobTransitionRequest` still has no source-attempt field. The remote
  transport must carry the captured ordinal. Its `ReadSnapshot` still returns
  scheduling data rather than the complete restricted execution snapshot.
- C1 still has no owner pending-to-failed policy edge or approval-SkipIf success
  edge. Typed debug activity and terminal output/manifest references remain
  required. The definitive memo source, zero-attempt materialization and remote
  import gaps are listed in the memo continuation.
- C2 authentication and delegation validators and C4 `ResolveConcurrency` remain
  panic stubs. Native assembly tests use contract fakes. Selected-engine host
  invocation, cross-process mask propagation and native placement callers remain
  real integration points.

W3 still owns native debug, memo materialization and admission composition,
production local/trigger/pod caller migration, and deletion of the legacy
scheduler, Session, Config, Execute, hosting layers and fixtures. These are not
waived. Production renewal adapters must connect `OwnerHost.Renew` to
`RenewUnit(..., executing)`. Fake callbacks prove the host projection, not billing
or authenticated transport integration.

## Deletion, shared files and friction

Legacy hosting code deleted in this continuation: **0 lines**. The removed
liveness-only interface requirement is **1 line**. The merge preserves W3's
prior `claim_exec.go` deletion without counting it again. No additional archived
test source row is claimed ported.

Shared caller edits are `internal/acceptance/host_checkout.go`,
`host_expansion.go`, `host_owner.go` and `host_planning.go`. Each supplies the new
renewal callback. Other shared files are C3's protocol contract, this report and
the two resolved inventory/handoff documents. No production caller outside
nodehost changed in this continuation. The lead-owned handoff is unchanged from
the integration branch.

Xwing environment inspection failed because its session-index lock is read-only
in this sandbox. The task continued in the user-selected environment. Bootstrap
ran the read-only installed Sparkwing info command before the swarm brief was
loaded. No subsequent Sparkwing executable, pipeline, candidate installer,
external service, Git write or release ran. The lead retains staging, independent
review, pipeline gates and native placement verification. Scratch is removed
after every check joins.

<!-- design:end -->
