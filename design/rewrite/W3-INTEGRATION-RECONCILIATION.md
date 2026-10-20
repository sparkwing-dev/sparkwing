# W3 integration reconciliation

All 23 top-level failures in the supplied integration log were reproduced and now
pass in a focused rerun across cmd/sparkwing, internal/nodehost and tests/acceptance.
No production security validator was relaxed. No failing test was skipped.

## Fixes and test corrections

Local intake's two fixtures sent the literal SHA `head`. Production preparation
already sends source.Acquire's Entry.SHA: a full commit ID, or empty for an unborn
local branch. C3's source contract requires those values and rejects revision
names. The policy test now obtains the SHA through real source.PrepareCheckout
for unborn, SHA-1 and SHA-256 repositories, then admits, executes and replays each
through the production local controller, with both development and release
versions. It verifies the source SHA survives intake. The cache-profile fixture
uses its actual committed HEAD. The producer and strict validator are unchanged.

Burst fixtures claimed the owner with an ordinary team-machine root, violating
W9 burst review S1. They now select RunnerHere, claim through the real intake's
here award and carry complete host capacity. Their sealed preferences name here.
The overflow claimant remains the requester's registered personal desktop.
Saturated availability goes through RecordHereAvailability under the live owner
fence. The 11-line burstSaturatedOwner fake was deleted. S1's claim and secret
restrictions remain unchanged, and the store's burst ownership and placement
security proofs pass.

The owner scheduler ignored persisted cancellation when the node had no active
local goroutine. Invariant: an unclaimed cancelled job settles without starting
an execution attempt. It now drains pending and ready jobs under its existing
store authority, and stops a local worker only when one exists. The regression
covers both states and keeps the cancellation reason. Foreign holders retain
their result rights.

Data POST parsing treated every query as a publication triple. Existing security
probes append foreign team, ref or key values to prove those values cannot select
authority. Invariant: unrelated query values cannot replace the authenticated
claim's run, job, attempt or generation. Publication parsing now starts only when
job, attempt or generation is present. The complete triple and DataPublish gate
remain mandatory. Malformed-query errors retain the decoded data evidence for
authentication. The original spoofed-selector assertions are unchanged. The new
fuzzer covers ignored authority fields, incomplete triples, claim attempts to use
a complete triple, and malformed query bytes.

The result replay fixture supplied source_attempt=0 against recorded attempt 2.
It now names 2, as required by the explicit source-attempt handoff and C2's exact
replay proof. The matcher is unchanged. Runner provisioning fixtures already had
team.admin but lacked runs.write, which the personal-runner route requires under
decision 116. They now carry the real administrator's complete route grants.
A non-admin refusal was added. The login machine fixture registers under an owner
session to satisfy Korey's team-admin registration rule before testing the
unchanged refusal to sign in with a machine credential. OIDC's console case now
expects not_found under the lead's 04:00 run-existence privacy decision. Its
provider-call count stays zero.

Secret reads checked cipher availability after the transactional read and release
audit. Invariant: missing bound encryption cannot read, audit a release or open a
stored secret. BoundCipher availability is now checked first, preserving C2's
existing unavailable result and the original zero-read/zero-open assertions.
The generated binding property includes absent and unbound ciphers. A separate
HTTP proof confirms ephemeral requester values still skip decryption, preserving
decision 116's local-secret path. No new configuration requirement was added.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and temporary paths under /tmp/sw-astra-W3.
Go tests use at most four package workers.

Passed:

- Whole-repository `go build ./...`.
- Focused vet of cmd/sparkwing, internal/nodehost, internal/controller and
  tests/acceptance.
- All 23 originally failing test names, including their original refusal cases
  and fuzz seeds. The final grouped run completed in 2.032 s, 0.957 s and 1.620 s.
- Full nodehost suite, 25.437 s. The strengthened pending/ready cancellation
  assertion was checked separately afterwards.
- The broader Data, SecretRead, RootProvisioning, Login, Engine, OIDC and data fuzz
  seed selections in tests/acceptance and internal/controller, 6.407 s and 6.064 s.
- The data-query fuzzer, 210 generated executions with two workers.
- Store personal-machine ownership, sealed burst placement, non-owner refusal
  and fenced here-capacity tests, 0.898 s.
- Commentcheck against main across 2,799 files and whitespace checks.
- Independent read-only review found no blocker or weakened security assertion.
  Its Simplify assessment found no new mechanism or unused abstraction.

No historical test was newly ported and no acceptance matrix cell was newly
enabled. This session reconciles existing tests with the recorded decisions and
fixes the three production regressions those tests exposed.

## Contracts, scope and volume

Contract changes made under integration authority: C2 clarifies authenticated
query scoping and the existing pre-read BoundCipher requirement, including the
ephemeral exception to decryption. C3 clarifies that unclaimed pending and ready
cancellation drains without a local worker. No interface, wire type, schema,
grant, setting, environment variable, cache, retry or timeout was added.
No published docs impact: these fixes restore the declared behavior without
changing the public surface. Lead-owned handoffs.md and amendments files were
read and left unchanged.

Shared files touched:

- cmd/sparkwing/local_controller_policy_test.go and local_profile_test.go.
- internal/controller/data_request_binding.go and secret_reads.go.
- tests/acceptance/data_reads_http_test.go, engine_lifecycle_http_test.go,
  login_http_test.go, oidc_mint_http_test.go, root_provisioning_http_test.go and
  secret_reads_http_test.go.
- design/rewrite/contracts/c2-credentials.md and c3-protocol-sdk.md.

W3-owned changes are owner_schedule.go, owner_admission_result_test.go,
store_host_fixture_test.go and burst_store_admission_test.go in internal/nodehost.
No API was removed and no production caller needed migration. Production changes
are +13/-11, net +2. The replaced 11 lines belong to the three fixes above.
W3-owned production versus main is +12,080/-26,049, net -13,969 under the lead's
exclusions, within the -2,300 budget. Earlier legacy cuts remain intact.

## Remaining DESIGN and native evidence

The previous report's DESIGN for SQLite follower cancellation remains a separate
handoff. Invariant: cancelling a follower after completed work cannot fail the
owner, and independent read or subscription errors remain observable. The
proposal puts cancellation normalization at C1's shared state-read boundary,
preserving independent and joined failures. No new follower special case or
foreign store change was made here. Approval and the real SQLite interruption
proof remain for the lead and C1. The unchanged reproducer
`go test -p 4 -race ./internal/nodehost -run
'^TestRetryExecutesCommittedSelectionAfterRestart/all$' -count=20 -timeout=90s`
passed in 49.177 s. A separate 60-iteration run with timeout=240s passed in
114.996 s. These 80 passing iterations do not establish a fix for the earlier
SQLite interruption failures. The lead can reassess that DESIGN with this
merged-state evidence.

W3-native-latest.md is green for bin and cmd/sparkwing after native session 2.
It predates this reconciliation. The lead harness owns the next native run and
full-suite confirmation of the merged edits. These local focused results do not
claim native socket, PostgreSQL, Windows or Darwin execution. No other contract
change or stub integration is left open by these fixes.

Friction: installed Overwing reports that it trails its source by 10 commits,
but matching and reading standards worked. No Git writes, operational Sparkwing
execution, install, pipeline, deployment or release ran. Git has no local identity
or core.worktree override. Scratch is removed after all checks join. The shared
build cache is retained.

STATUS: CONTINUE
