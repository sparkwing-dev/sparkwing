# W2 integration continuation

Resolved all four conflict files against the integration branch. The working
files contain no conflict markers. The index still marks them unmerged because
the swarm brief forbids Git writes. The lead must stage the resolutions and
finish the merge. HEAD remains 8222a527a.

The amended blob lookup and memo authority checks survive. Console finance now
uses W1b's transaction-local freeze helpers. Operator and console settings share
one rate-table-preserving helper. Removed APIs stay removed, including
SetOperatorCreditSettings, HoldTeamForDispute and SyncLaunchJobs. The merged
legacy launch-release test stays deleted. Its native reservation/refund and
started-release coverage passes.

Items (a) to (e) have the following state. Remaining work requires the other
owners' code or contracts listed here.

| Item | State and evidence |
| --- | --- |
| (a) Committing origin gate | Complete from the earlier commit. TestCommittingAccountOriginProperty passes against both the root-issuance and owner-consent transactions after the merge. Both use DirectAccountSession. |
| (b) Production adapters | Bootstrap seed/readiness, native console, verified local peer, launcher snapshot and data authority/access adapters are implemented. This session also connects log reads to the configured data backend and preserves the holder/generation through the shared engine actor. Non-checkout proofs reach W1b's merged SettlePayment implementation. Its final receipt/correlation schema and trusted setup writer still wait on W1. |
| (c) Declared pipeline references | Complete from the earlier commit. Native declared-reference, latest-run and HTTP tests pass. |
| (d) Dashboard source and resume | Complete from the earlier commit. Canonical source resolution and native resume/authority tests pass. |
| (e) Missing registrations | Nineteen of the original 23 registrations exist. Four still wait on C1/W1b/W7/W9 contracts below. Queue-priority HTTP is implemented against QueuePriorityWriter, but C1 has no integer-priority persistence/ordering transaction. No policy row or feature was removed. |

The requested controller repairs still pass: seven socket-free metrics tests,
trigger validation and receipt not-found. The earlier two obsolete tests remain
deleted. Dedicated metrics listeners need the lead's socket-enabled environment.

W1's separate cancellation snapshot is integrated. Authentication gives job
intent precedence over run intent, including an empty job reason. Cancellation
and holder-result rights remain independent. The generated snapshot and HTTP
holder-preservation tests pass.

The W2 amendment work retained from prior sessions covers the shared inner-error
codec, process policy rows, selected claims and here reclaim, trusted roots,
store authentication snapshots, binding evidence, cancellation/failure routes,
source/plan-secret adapters, billing verification and data signing. The merge
adds no new amendment row assigned to W2. W3/W8/W9 and T1 retain their process,
runner, transport and native-topology integrations. Lead-owned handoffs and
amendment files were not edited.

Native log reads now use the same configured data backend as signing and commit.
The prior read adapter dropped the claim's holder and generation. It now uses
engineActor, so the store can recheck the actual fence. Missing log storage
returns the canonical unavailable error. Log commits retain decoded Size and
compressed StoredBytes separately. Streams retain Cache-Control: no-store.
Bootstrap setup takes BootstrapAdminIssuer directly. Its test double no longer
embeds an unrelated Store with promoted production methods.

The authorization invariant is that a refused log read performs no physical
read, delivers no log bytes and incurs no egress charge. Generated native HTTP
cases cover foreign run/job paths, stale holders/generations, unknown
presentations and removed membership. Native root revocation uses the real
transaction and blocks the next read before physical I/O. Header mutations
cannot select another principal's concurrency slot.

The eight retained egress tests now use native claim awards, sealed account
sessions, real SQLite segment commits and final job-log routes. They retain
reader isolation, authentication, bodyless refusal accounting, slot release,
stream concurrency, daily alarm privacy and header isolation. HEAD remains
unregistered. The old live/dashboard stream aliases remain absent. Object I/O
is fake in these HTTP tests. The new tests are component proofs, not native
placement matrix cells.

The egress test file shrinks from 418 to 313 lines, net -105. No production file
was deleted in this continuation. Relative to the previous W2 commit, the merge
and resolutions shrink credit_reversals.go by 216 lines and
operator_credit_settings.go by 20. Those counts include incoming W1b retirement.
No replacement credential verifier, compatibility alias, schema, setting,
customer cap or new service was added.

The four missing registrations are still reported in both metrics configurations:

- GET /api/v1/runs/{run}/jobs/{job}/output
- GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output
- POST /api/v1/runs/{run}/jobs/{job}/claim/input
- POST /api/v1/operator/teams/{team}/refunds

The new NodeRow.OutputRecord/ManifestRecord selectors close the old projection
gap. They do not supply a member-session blob read edge, the child blob read
edge, the claim-input request/projection, or the human-refund request joining
console authority to provider/ledger owners. Those contracts are still needed.
QueuePriorityWriter also needs W1's persisted integer priority and ordering
contract. No dummy handler was added to make the inventory green.

Other real integrations remain with their owners: authority-bearing local and
credential/account/team mutations, roleless sessions, trusted schedule/App
intake and local-first matching, runner observation projections and final
remote tagged-log transport. W7's LogTransport/SegmentRecorder interfaces now
exist, but the remote verification and owner-read wire operations still need
C2/W9 agreement. The existing job-log HTTP and transactional commit paths are
covered here. No full remote uploader integration is claimed.

Shared store files resolved or adapted in this session:

- pkg/store/blob_data_access.go
- pkg/store/claim_run_test.go
- pkg/store/console.go
- pkg/store/console_test.go
- pkg/store/credit_reversals.go
- pkg/store/operator_credit_settings.go

Other shared changes are internal/web/reads.go's log-stream cache header,
docs/observability.md, docs/migrations/_unreleased.md, CHANGELOG.md, generated
mirrors and .apidiff/pkg_controller.txt. Controller edits are bootstrap_token.go
and its test, browser_reads.go, egress_test.go, log_segments.go and
log_submission_routes.go and its test. C2's source contract and the port
inventory record the changes. No external caller needed a source change for
the narrower bootstrap interface because the controller startup passes Store.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W2. Tests used
at most -p 4.

- Whole-repository build and scoped store/controller/web vet pass.
- Full capability, apierr, apiroutes and controller/client suites pass.
- Focused store origin, console, cancellation, authentication, data authority,
  settings, references, latest-run, release and payment-transaction tests pass.
- Focused controller bootstrap, console, peer, launcher, source, references,
  resume, cancellation, logs, egress and requested repair tests pass.
- The native-log authority, stream-cap, header-isolation and signed-log commit
  selection passes under the race detector.
- W2 credential/payment/source/session executable acceptance selection passes,
  including the existing credentials-operator-console-split/store-sqlite and
  credentials-session-digest-csrf/store-sqlite proofs. No new matrix cell is claimed.
- Native TypeScript succeeds with go -C sdk/typescript test -p 4 ./test/nativehost
  -run TestNative -count=1 -timeout=90s.
- Focused web log/stream tests, commentcheck against main, doc mirror sync,
  changelog and diff whitespace checks pass.
- Independent read-only review found no introduced defects in the resolutions,
  log/egress adapters or bootstrap interface change.

TestFinalCredentialServingRegistrations remains red for the four routes above.
A broad store selection also reached the known Cloud-root metering defect
assigned to W1 by the 15:40 decision and old token/trigger fixtures outside this
change. No full store suite is claimed. Broad controller/web selections reached
socket-based tests and stopped with listen tcp6 [::1]:0: socket: operation not
permitted. Their focused socket-free selections pass. No full controller/web
suite, PostgreSQL, provider deployment or native placement pass is claimed.

W2 production Go versus main is +14,350/-17,191, net -2,841, within the -250
budget, using the lead's ownership exclusions.

Friction: Xwing context inspection fails with mkdir
/home/bombuntu/.xwing/locks/sessions.index: read-only file system. The explicitly
selected environment remains in use. Overwing reports its installed binary is
10 commits behind source. Git metadata remains read only. Default Git identity
is configured and core.worktree is unset. No Git write, provider network call,
pipeline or runtime Sparkwing process ran. Initial agent-info discovery was
read only. Scratch is removed after checks. The shared build cache is retained.

STATUS: CONTINUE
