# W2 final serving continuation

<!-- design:begin -->

Decision 114 was already committed at session start. Its operator route, CLI,
atomic team/admin issuance, operator audit, duplicate refusal and docs/auth.md
remain implemented. This continuation closes the three registrations left in
W2-operator-team-handoff.md. All declared controller serving registrations pass.

## Implemented and removed

- Native job output reads share the child output reader. The store reloads
  ancestry, membership, binding and the committed result record in one snapshot.
  The configured blob backend verifies the JSON bytes. The native SDK client
  consumes the shared JobOutput shape. Current member reads also work.
- The console refund route records an already completed provider refund through
  consoleWrite and reversePaymentTx. It rechecks current operator authority,
  resolves the persisted payer and refuses a different path team. The reversal,
  operator audit and exact request receipt commit together. Provider webhook
  settlement shares the refund reference, preventing a second debit.
- Removed the stale claim-input policy because C1 already deleted that reader
  and its wire types. Migrated the local credential probe to the existing
  own-job data-authority policy with the same grant, binding and sensitive
  liveness. Updated its acceptance port mapping. No alias or fallback remains.
- Updated stale policy-fixture facts for bootstrap delivery, deployment scope,
  source-report leases and owner processes. Updated the encoded-path property
  to expect the existing cross-team not_found boundary.
- Generated the output, refund and team-create OpenAPI bodies. Updated source
  API/migration documentation and embedded mirrors.

No legacy implementation file remained to delete in this slice. Removed or
replaced 15 production Go lines, including the retired claim-input policy and
repeated output response declarations. Session production delta including
shared files is +186/-15, net +171. W2 alone is +52/-6, net +46.
Against main, W2 is +16,612/-17,420, net **-808**, within its -250 budget.
Counts include untracked production files and exclude tests, testdata,
acceptance harnesses and contract declaration files.

The output invariant is: only a committed output selected under current
member authority or the declared read relation can yield bytes. Generated
native-store cases vary team, run, job, read declarations, revocation,
cancellation and generation. Real HTTP also refuses removed membership.

The refund invariant is: only a current configured direct console session can
reverse its path team's persisted payment, and every successful reversal has
an atomic operator receipt. Generated store and HTTP cases vary authority and
selectors. Tests also cover an existing foreign team, audit rollback, exact
replay, changed replay, revocation and subsequent provider delivery.

## Contract changes made

- C1: ReadJobOutput shares the existing current-authority output selector.
  Console.ConsoleRefund adds a console-gated transaction over existing ledger
  and business-event owners. No schema change.
- C2: removed retired claim-input policy and declared final output/refund
  composition, current authority, payer check and receipt semantics.
- C3: JobOutput, OperatorRefund and OperatorRefundRecorded, plus the shared
  native client response. Existing policy retry and console rules remain.
- T1: retired claim-input source-route disposition and surviving own-job port.

Mechanism added: the console refund receipt lives in business_events because
ledger reference deduplication alone cannot reject changed bodies under the
same HTTP idempotency key. Removing that receipt loses exact replay checking.
No new table, setting, environment variable, credential authority or service.
The chosen refund request records an externally completed refund and makes no
provider request. C1's existing reversal semantics and webhook owner support
that path. Provider refund initiation is not implemented by this operation.
The lead should review that wire choice with these recorded amendments.

## Evidence

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W2, with at most four package workers.

Passed:

- Whole-repository go build -p 4 ./....
- Focused vet for capability, apierr, controllerwire, apispec, apiref,
  localcontroller, store, controller, controller/client, acceptance and CLI.
- Full capability, apierr, controllerwire, apispec, apiref and localcontroller
  tests. Focused controller policy, credential, console, output and operator
  tests (8.492s), plus store binding, bootstrap, refund and console regressions.
- Credential acceptance selection, including authentication, policy alternatives,
  issuance, inventory, mutations, maintenance/default transactions and the
  local-node authority start port (2.902s). No native topology cell was unskipped.
- Output, console refund/default composition and operator team-creation tests
  under -race: controller 9.625s, store 1.687s.
- CLI team creation through the real client/router/authenticator/store, operator
  client tests and auth-guide grammar/vocabulary checks.
- API spec/reference generation checks, embedded-doc privacy/symbol/drift checks,
  sync-docs --check, commentcheck against main and git diff --check.

No historical test was deleted. The own-job claim-input regression now exercises
its surviving data-authority replacement. The new output test drives native
process output through the SDK client and real filesystem bytes.

A broader controller selection stopped at the socket restriction in
TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader. No full controller
or repository test pass is claimed. Native sockets, PostgreSQL, provider network
and placement/topology QA remain lead-owned evidence. QueuePriorityWriter still
needs C1's persisted integer-priority/ordering transaction. Its existing HTTP
contract uses a fake and fails closed without the adapter. No additional
contract change is requested for this slice.

## Shared files

Outside W2: internal/store/child_output.go, console_refund.go and their tests;
internal/wingd/localcontroller/claims.go; internal/controllerwire/operator.go
and output.go; internal/controller/client/native_services.go; internal/apispec/wire.go;
credential/local-node acceptance fixtures and their port documents; C1/C3
contracts, API/OpenAPI/migration docs and embedded mirrors.

Friction: the broad selection encountered stale fixture facts and a stale 403
expectation, both corrected against the current contracts. Socket tests cannot
run here. Git rename detection warns on the large main diff, but line counts
and diff checks complete. No Git write, installed binary, service or pipeline
ran. W2 scratch is removed at handoff. The shared cache is retained.

<!-- design:end -->

STATUS: COMPLETE
