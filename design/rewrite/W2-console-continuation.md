# W2 console and queue continuation

The native console adapter implements all nine ConsoleWriter operations. Default
HTTP composition reaches actual SQLite commits. Queue priority now has its HTTP
handler against a C2 transaction interface. The serving inventory has 19 of the
23 registrations missing at the previous review. Four registrations remain open.

## Items (a) to (e)

- (a) The prior committing origin gates remain complete. The generated store
  property passes again. Root issuance and owner consent require the same live
  direct account-session predicate in their committing transactions.
- (b) Store.AsConsole supplies the production ConsoleWriter with compile-time
  conformance. It checks the live direct session, exact account identity and the
  server's configured operator gate inside each transaction. A second gate check
  before commit rolls back an operation if the operator was delisted. Audit
  actors come from that session. Bootstrap, peer, launcher and data adapters
  remain implemented from the previous session. Non-checkout payment proofs
  reach SettlePayment. W1b still owns their settlement implementation.
- (c) The native declared-reference relation and latest-run query were already
  implemented. This session makes no further change to them.
- (d) Canonical dashboard repository resolution and resume were already
  implemented. This session makes no further change to them.
- (e) Queue-priority HTTP validates the existing request, preserves explicit
  zero, ignores caller team/actor hints and forwards the idempotency key through
  QueuePriorityWriter. Its generated HTTP property uses a fake transaction.
  W1 still needs persisted integer priority and an ordering transaction. The
  missing four routes remain job output, child job output, claim input and
  console refunds. Their C1/W1b result, read-edge, input and refund contracts
  remain the blockers described in W2-serving-handlers.md.

The requested controller repairs remain in the earlier commit. The seven
socket-free metrics tests, trigger validation and receipt not-found pass here.
The earlier report records ten retained repairs and two obsolete tests deleted.
The dedicated metrics listener still requires a socket-enabled lead check.

## Native console behavior

Finance writes share the existing ledger helpers inside the authorized
transaction. Grants retain reference deduplication and actor derivation. Trust
retains its limits and audit. Empty-hold unfreeze releases only operator holds.
Explicit unknown holds return not_found. Credit settings preserve installed
price-table ownership. Installation unbind resolves ownership in the transaction
and disables its dependent schedules with the store clock.

Waitlist approval selects and commits one batch with a replay marker. A changed
request under the key conflicts. Completed replays return no newly admitted
accounts and send no additional notifications. The marker contains only a request
digest under a hash of operator and key. It stores no account snapshots.

Account deletion resolves an exact ID or unique email inside the transaction.
The former token-table revocation is replaced by native capability revocation,
including envelope and generation updates. Team deletion records cancellation
through the shared run/job transition owner. Live jobs retain their holder,
generation and attempt. Only the internal deletion transaction can establish
that actor's selected-team scope. An externally supplied empty operator actor
is refused. Global admission audits use nullable team ownership from the final
schema and retain kind/subject deduplication through their primary key.

The authority property generates derived, expired, revoked, missing, bearer,
substituted-account and delisted presentations across all nine mutations.
Refused calls leave finance and deletion state unchanged. A configuration change
before commit rolls back both the hold and its audit.

## Deleted and shared files

Removed the 18-line old token revocation helper and the 22-line direct
trigger-cancellation implementation. The account deletion path no longer queries
or updates tokens, selects the old token principal, or references trigger_user.
No production file was deleted in this continuation. The production diff removes
134 lines, including bodies moved into shared transaction helpers.

Shared production store files:

- billing_trust.go
- business_events.go
- console.go
- credit_reversals.go
- credits.go
- deletion.go
- dialect.go
- github_app.go
- operator_credit_settings.go
- signup_gate.go
- state_authority.go

New shared tests are in internal/store/console_test.go. Controller changes are
console_writes.go, console_writes_test.go, console_store_runtime_test.go,
queue_priority.go, queue_priority_test.go, queue_view.go and server.go. C2's
source contract and the two W2 reports are updated. Lead-owned handoffs and
amendments are untouched. No cross-package API was removed, so no additional
caller migration was required.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and
GOTMPDIR/TMPDIR under /tmp/sw-astra-W2. Test package concurrency stayed at -p 4.

Passed:

- Whole-repository go build -p 4 ./... and scoped store/controller vet.
- Native console store and default HTTP tests, queue-priority property, committing
  origin property, authentication snapshots, bootstrap, launcher sync, local HTTP
  and cancellation/holder preservation tests.
- Seven socket-free metrics tests, trigger validation and receipt not-found.
- Full capability, apierr, apiroutes and controller/client suites.
- The W2 credential/payment/source/session executable acceptance selection.
- Native TypeScript: go -C sdk/typescript test -p 4 ./test/nativehost -run
  TestNative -count=1 -timeout=90s. Dependencies and native setup work.
- Commentcheck against main and diff whitespace checks.

TestFinalCredentialServingRegistrations still reports exactly four missing
routes in both metrics configurations. No new native topology matrix cell is
claimed. Queue mutation persistence and the four missing routes are not claimed
as working production integrations.

A broader controller run stopped in TestDrainGitHubChecks_DoesNotInheritASpentBudget
when httptest could not create a listener. Broader focused selections also reached
socket-based object-budget and repository-URL tests. The error is listen tcp6
[::1]:0: socket: operation not permitted. No full controller pass is claimed.

A broader store selection reached three unrelated fixture failures:
TestOperatorTrustGrantAndRevoke has no payment_mode for OpenCreditCheckout.
TestADisputeIsBoundToOnePayment still attempts a raw paid grant.
TestGitHubAppUnbindDropsTheTeamsSubscriptions omits github_app_triggers.repo_key.
These fail in unchanged paths before the transaction helpers this session
extracted. The native console tests cover trust, holds, unbind and deletion
against the final schema. The selected retained freeze, settings, waitlist,
account-selector and last-owner tests pass.

## Contract changes and integration points

C2 adds only its local QueuePriorityWriter declaration. W1 must supply its
persisted-priority schema and ordering contract before a production adapter can
conform. No C1 signature or schema was changed. W1b/W7/W9 still need the committed
output/input projections and member/child blob edges. Human refunds still need
the console-authorized provider/ledger request contract. Roleless direct console
sessions still need W1's representation. The previous report retains the other
cross-area integrations, including authority-bearing local mutations and data
session/expansion projections. Native console mutation composition is now closed.

Mechanisms added: AsConsole binds the existing operator configuration to one
transaction boundary because the unscoped break-glass Operator cannot validate
HTTP session authority. A durable approval marker is required because a retry of
oldest-N must not select another batch. The internal deletion scope lets deletion
use the existing transition owner after memberships and sessions are removed.
Global audit identity preserves deduplication with the schema's nullable team.
Each can be removed with its corresponding adapter change. No configuration
option, environment variable, dependency, schema column or compatibility alias
was added.

W2 production delta versus main is +14,181/-17,001, net -2,820, within the -250
budget. This continuation adds 66 net W2 production lines. Shared store edits add
512 and delete 129 production lines, net +383. Tests, acceptance harness and
contract declarations are excluded under the lead's measurement rules.

No Git write, Sparkwing process, provider network call or pipeline ran. The lead
owns committing. Scratch is removed at handoff. The shared build cache is kept.

STATUS: CONTINUE
