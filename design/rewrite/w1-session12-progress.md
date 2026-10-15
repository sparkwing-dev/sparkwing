# W1 continuation

Starts at a41e5f6bd. W1 remains incomplete. W1b implementation files are unchanged.
The lead owns review, Git writes and landing.

## Merge

The only conflicted file was contracts/handoffs.md. Its W1 continuation note and
the integration branch's SDK diagnostic-sanitizer decision are independent.
Both remain, with all conflict markers removed. The index still reports UU
because the swarm brief prohibits Git writes. The lead must stage that resolution
and finish the merge. No amended signature was replaced.

## Implemented and deleted

Retired Store.PruneSpentIdentity and Store.ExpireSessions, IdentityPrune and
SpentIdentityRetention. This removes **70 legacy store implementation lines**,
including the obsolete token and GitHub-runner table sweep. Removed the separate
**10-line controller identity sweep**. Controller maintenance already calls
Operator.MaintainRetention under its elected maintenance fence.

The final pass now handles invitations as well as sessions, flow nonces and App
delivery receipts. Each table observes the requested limit. Invitation retention
keeps the existing thirty-day spent-row window. Session expiry preserves rows
named by capability parents. RetentionResult gains Invitations and Sessions.
Any failure or expired fence rolls back every deletion and returns zero counters.

Invitation and invitation-email timestamps now use nanoseconds through creation,
listing, acceptance, withdrawal, membership-related withdrawal, account/team
deletion and email claims. Other legacy identity timestamps remain outside this
change. No schema backfill or compatibility reader was added.

Deleted **112 old test lines**: identity_prune_test.go and
node_retry_claim_generation_test.go. The identity preservation assertions now run
through the final leased API. The generation assertion is already stronger in
the active unstarted-lapse and reconciler SQLite cells: reaping retains the exact
generation and the next award advances it by one.

Added four final-schema store tests. A 64-case generated property preserves
live sessions and capability-parent rows under bounded cleanup. Other tests
cover invitation windows, repeated passes, rollback, expired maintenance fences
and public invitation timestamp round trips. No sleeps or external services.

Added TestRecovery/recovery-application-retry-ordinal/store-sqlite. It starts an
attempt at one, retries after a failure, and finishes attempt two under the same
holder, capability, generation and lease. Reopening preserves both completed
attempts and zero disruptions. Terminal replay creates no duplicate event and a
conflicting result fails. The exact events are job_started, job_retrying and
job_succeeded. This proves the scenario's one-failure path at the store boundary.

Strengthened credentials-session-digest-csrf/store-sqlite with forged-fence
refusal, expired-session cleanup, preservation of a live owner session and
another reopen. Native browser and process cells retain their required skips.

## Contract change needed: retry attempt fence

Invariant: a report affects only the attempt it names, and replay cannot consume
another attempt. NodeTransition and JobTransitionRequest have no source attempt
ordinal. Claim generation stays constant across application retries. Two identical
successive failures therefore have the same replay digest. The second failure
returns Replayed:true and leaves the previous attempt running. A delayed report
also cannot identify its old attempt after a retry.

The proposal is an explicit source attempt in C1/C3 transitions, transported by
W3/W9 and checked by W1 in the result transaction and replay identity. Start,
unstarted refusal and non-holder transitions need explicit semantics. This needs
the shared contract amendment before implementation. No foreign type was changed.
This is a launch blocker, independent of the passing single-retry proof.

The preserved source is w1-retry-attempt-probe.go.txt. To reproduce without
changing repository Go files, create /tmp/sw-astra-W1 and use a Go overlay:

```sh
mkdir -p /tmp/sw-astra-W1/cache /tmp/sw-astra-W1/tmp
python3 - <<'PY'
import json
from pathlib import Path
repo = Path('/home/bombuntu/.xwing/envs/1079/sparkwing')
overlay = {'Replace': {
    str(repo / 'pkg/store/retry_attempt_fence_probe_test.go'):
        str(repo / 'design/rewrite/w1-retry-attempt-probe.go.txt')
}}
Path('/tmp/sw-astra-W1/retry-probe-overlay.json').write_text(json.dumps(overlay))
PY
env GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4 \
GOCACHE=/tmp/sw-astra-W1/cache GOTMPDIR=/tmp/sw-astra-W1/tmp \
TMPDIR=/tmp/sw-astra-W1/tmp \
go -C /home/bombuntu/.xwing/envs/1079/sparkwing test -p 4 \
-overlay=/tmp/sw-astra-W1/retry-probe-overlay.json ./pkg/store \
-run '^TestRetryAttemptFenceProbe$' -count=1
```

Both direct and overlay probes fail with:

```text
failure of attempt 4: got {Replayed:true CancelRequested:false StateSeq:1 Generation:2 Attempt:4}, err=<nil>; want new attempt 5
```

The failing probe is excluded from the normal suite because it requires an
unapproved cross-area contract change. Its source and observed failure remain
reviewable. No production workaround, RetryAt-based identity or random replay
salt was added.

## Amendment audit and remaining integration

The earlier W1 implementation and evidence remain in the session 11 handoff.
This continuation checked these amendment groups against the code and contracts.

| Rows | W1 state and remaining owner |
| --- | --- |
| A1-07, A1-44, A1-45 | Acceptance/expansion use shared resolution, preserve classifications and persist per-job winners. W6 concurrency resolution still needs real integration where its contract remains a stub. |
| A1-08, A1-18, A1-55 | Mixed-kind scan, zero initial ordinal, authoritative Applied.Attempt, here first-claim/reclaim checks and one-time bearer rules are implemented. The newly reproduced retry-attempt gap needs C1/C3 amendment. |
| A1-11 | Trusted source location is sealed and retained through retry. W2 supplies the real intake policy. |
| A1-13 | MaterializeOnly dispatches on a miss without an execution award. W1b must join authorized record copying through materializationRecords in the same transaction. |
| A1-14, A1-15 | Persisted failure reads and scoped cooperative cancellation are implemented. The wire result still cannot carry the complete recorded JobFailure stage. |
| A1-16, A1-17 | Private execution state and exact persisted expansion bodies/current rows are projected. GuardRefusal and generation-zero materialized generator fences remain contract gaps. |
| A1-20, A1-25, A1-26 | Store provisioning, plan secret opener and enrollment boundaries are implemented and tested with contract fakes. W2 must install trusted origin, secret, intake and enrollment adapters. |
| A1-21, A1-22, A1-23 | Digest/session/ancestry/binding persistence is implemented. C2 still lacks BindingState cancellation reason and the committing result-body digest/operation transport. Console/peer/anonymous origins and production middleware remain W2 work. |
| A1-24 | Transactional binding, data-check integration and audited source release are implemented. Real source policy/decryption and App issuance remain W2 integration. |
| A1-27 | W1b's record/start/finish/charge/settle helpers exist. Claim lifecycle still needs the shared reservation, unstarted-refund and customer-wait integration. Metered awards fail closed; C2 SettleBilling is a panic stub. W1b files remain unchanged. |
| A1-28 | W1 committed-checkout settlement is implemented. W2 must retire its old caller-selected credit-grant path and supply verified provider events. |
| A1-29, A1-31 through A1-35 | Blob/upload/record/accounting ownership is W1b. W1 consumes transactional DataAccessChecker. Result attachment still needs nodeResultRecords from that owner. No W1b implementation was edited. |
| A1-38 | SyncLauncher still needs immutable Kubernetes/claim-generation correlation from W8/W9. Its declaration remains a stub. |
| A1-40 | Store handles exist. W8 owns LocalRuntime wiring/readiness and native lifecycle. |
| A1-51 | Legacy StateStoreOld, Store.AcceptPlan/validator, old claims/triggers, offer rounds and remaining unleased maintenance still require hard cuts and caller migration. This continuation retires the identity sweep only. |
| A1-53 | T1/lead still own native HTTP/api.sock, process, platform, PostgreSQL, provider and real-repository evidence. Store cells do not replace those proofs. |

Additional inherited contract requests remain: W9 ReuseSource cannot identify a
materialized result by job/state sequence; C2 Target cannot name the resolved
concurrency key; onboarding has no teamless-session representation. None was
silently redefined here.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test/build package parallelism is -p 4.

- Whole-repository go build ./... passes after the final changes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The exact store selection passes **317 top-level tests / 917 cases**, no skips.
  Its file list is w1-session12-tests.txt. Extract Test function names, omit
  TestPostgres-prefixed functions and join them into an anchored -run alternation.
- Controller TestEngine.* and TestSessionRoutePreservesCapabilityScopeProperty
  pass **34 top-level tests / 57 cases**, no skips. Controller/client packages compile.
- Full tests/acceptance passes **20 top-level tests / 489 executed cases** with
  **2,991 required skeleton/platform skips**. Full pkg/match passes 14 tests / 42 cases.
- commentcheck -base main is clean across 951 Go files. sleepcheck -base HEAD and
  git diff --check pass. Changed Go files are formatted.
- The four removed public declarations are absent from source and generated
  output. RetentionResult matches generated API output. Other inherited snapshot
  drift remains the aggregate generation owner's work.
- Git has no local user.name, user.email or core.worktree override.

The broader invitation selection failed in legacy ResolveSignIn fixtures before
reaching invitation behavior, with a signup-admission foreign-key failure. An
overbroad controller test expression selected two removed-table failures outside
the final adapter selection: TestStoragePassCountsLogSegments (sparkwing_meta)
and TestStorageWritesUnboundedWithoutAQuota (tokens). It also selected
TestBillingTrust_OperatorGrantsAndRevokes, which could not run because its
httptest listener is forbidden by the sandbox. These outcomes are not counted
as passing evidence. No full legacy store/controller suite pass is claimed.
The final-schema scoped checks above pass.

The retry overlay probe is an expected failure documenting an unresolved contract
defect. PostgreSQL, race, lint, native platforms, providers, pipelines and
independent review remain lead checks.

## Shared files and friction

Production caller migrations outside pkg/store are pkg/controller/storage.go and
team_storage.go. The minimal fixture migration is
pkg/controller/credit_reversals_test.go: it directly deletes the legacy token row
to exercise its existing missing-row assertion without calling the removed prune
API. That broader fixture still requires W2's token retirement.

Shared acceptance files are internal/acceptance/store_recovery.go,
store_sessions.go and the new store_retry.go. Shared docs are .apidiff/pkg_store.txt,
contracts/c1-data.md, handoffs.md, t1-acceptance.md and t1-port-inventory.md.
The other changes are W1 store files and W1 handoff/probe artifacts.

Friction came from legacy seconds, removed-table fixtures and the unfenced retry
contract. The SQL guard caught an exemption made obsolete by extracting identity
retention; it was deleted and the guard passes. The initial tests correctly failed
compilation before RetentionResult gained its counters.

Bootstrap discovery ran installed sparkwing info --for-agent before the brief was
loaded. No Sparkwing run, service command, candidate, deployment or release ran.
No Git write or harness-memory write ran. Scratch and owned check processes are
removed before reporting. No file outside the worktree or /tmp/sw-astra-W1 was
written.

STATUS: CONTINUE
