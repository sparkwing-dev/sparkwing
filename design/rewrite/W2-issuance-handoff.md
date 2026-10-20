# W2 credential issuance continuation

<!-- design:begin -->

## Implemented

All three initial merge conflicts have resolved contents. The concise incoming
CreateRunResponse documentation remains. W2's replacement run-detail path remains,
so the incoming legacy handleGetRun/fillRunTrigger functions and
finish_run_recovery_test.go stay deleted. Incoming work in other areas remains.
Git still lists the paths as unmerged because the brief prohibits Git writes.
The lead must stage the contents and finish the merge.

POST /api/v1/tokens uses PolicyRouter. An account session provisions its own
member root through Store.ProvisionCapability. A member bearer delegates through
Store.DelegateCapability and retains its parent. Both wrappers commit through
Store.Issue. The body accepts an explicit grants array and optional absolute
expires_at. Authentication supplies team, subject and issuer. C1 rechecks current
membership, authority, grant narrowing and expiry in the issuance transaction.
A delegation with omitted expiry inherits the parent's expiry.

The response carries capability, prefix, team, grants and optional expires_at.
Errors never include a returned bearer, including an issuer returning a bearer
alongside an error. Responses use no-store. The dashboard member caller uses this
route and shape. It no longer requests the removed triggers.read grant.

The local C2 contract correction is creation's BindNone in place of ownToken.
C1 requires a token selector for ownToken, while creation has none. Token-specific
operations retain ownToken. Retry is never because this issuer has no secret
replay/idempotency contract. The independent T1 literal and route appendix record
the correction. No other area's contract was changed.

CredentialIssuer is the added mechanism. It preserves C1's existing two method
signatures and defaults to the server's Store. An injected implementation lets
HTTP tests exercise committed/refused issuance without implementing another SQL
owner. WithCredentialIssuer removes that injection when passed nil. Keeping only
a concrete Store would prevent isolated HTTP refusal tests. Undoing the injection
removes the interface, setter and field while keeping the default calls.

The merged W4 validator closes the earlier plan-secret stub gap. New tests use
the real bound cipher, C1 plan transaction, W4 validator and W6 settings resolver.
A valid plan persists. Property checks reject a decrypted stored secret in job,
step, group and concurrency identifiers with secret_in_identifier. Errors omit
the value, and no plan or jobs persist on refusal.

## Deleted and ports

Deleted 88 lines of legacy mint types/helpers: 64 global mint lines in tokens.go
and 24 member helper lines in team_tokens.go. Their old dispatch/registration
and two unused imports are also removed. The remaining legacy team-token mint
only handles machines. The broader old credential stack, listing, rotation and
machine path remain unfinished.

The new HTTP tests port component behavior for authenticated personal issuance,
transaction-time membership removal/demotion, expired/revoked session refusal,
exact requested grants, delegation ancestry and grant widening refusal.
The old combined token tests still contain listing, administration, machine and
PostgreSQL findings. They are retained until their final paths are covered.
No full old credential matrix or native T1 cell is claimed ported.

The encrypted plan property ports controller_accept from
TestAcceptPlan_RefusesASecretInANodeID and extends it to step/group/concurrency
names. It supplements the existing envelope-relocation fuzz test. SDK defaults,
expansions and native placements remain separate acceptance obligations.

## Amendment dispositions

No handoffs.md entry assigns W2. Earlier completed work is preserved in
W2-source-handoff.md, W2-data-http-handoff.md and W2-amendments-1-handoff.md.

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Shared decoder and node-service policies remain implemented. W3 owns actual process transport and registration. |
| A1-08/09/19/26/55 | Existing mixed/named claims, here ancestry/window rules, enrollment and version boundaries remain. Concrete enrollment and W3/W8/W9 execution identity/transport integration remain. |
| A1-11/16/17 | Sealed source and private execution projections remain. Consumer cutovers remain with their owners. |
| A1-14/15 | Failure reads and holder-preserving cancellation remain. Cancellation intent reason still needs C1's separate field. The existing reason test fails. |
| A1-20 | Personal member roots and ordinary member delegation now reach the sole issuer through HTTP. Bootstrap, machine, service and Actions provisioning and token listing/rotation remain. |
| A1-21/22/23 | Existing fresh snapshot verification and route evidence remain. Token creation joins actual policy registrations. Default browser/console/peer and the remaining route cutovers stay open. |
| A1-24 | Stored source release remains connected to C1's audit transaction. App release permit and data authority/metadata/charging projections remain open. |
| A1-25 | Stored-secret opening now passes real shared plan validation, including safe success and secret-identifier refusal. Trusted production intake adapters and native host evidence remain. |
| A1-27/28 | Existing immutable billing provenance and signed committed-checkout callback remain. Production payment configuration and other verified billing-event contracts remain open. |
| A1-29/30/31/32/33/34/35 | Existing authenticated generic data routes remain. Persisted authority/read/charge projections, physical backend transfer and specialized memo/depcache transport remain. |
| A1-38 | Launcher sync retains its declared fake-backed boundary. C1's final production snapshot remains open. |
| A1-39/40/49/53 | Concrete runner/local-controller startup, credential-file handling and native topology coverage remain with W3/W8/W9/T1/lead. |

## Contract changes needed and integration left open

- C1 must populate the session presentation digest and replace legacy session/CSRF
  persistence. The default verifier still refuses the empty session digest. The
  real issuer HTTP tests supply the missing digest through the existing test-only
  reader fixture and a session-bound CSRF checker. No production bypass was added.
- C1 must provide an audited App release permit carrying verified installation,
  repository and approved-extra facts before W2 can mint the external token.
- C1 must separate cancellation intent reason from NodeResult. W2 keeps result
  fields empty when requesting cancellation of a live holder.
- C1 must expose committed credential metadata/list selection and authority-bearing
  rotation. Its ownToken checker requires an existing selector, so the remaining
  declared collection GET also needs its final account-scoped read contract.
- The previous data authority/live-blob/download-charge projections, launcher sync,
  authority-bearing operator writes and sizing writer remain open.
- W2 still needs final trusted browser/console/peer adapters, other provisioning
  origins, source/secret/log/account/queue route hard cuts and specialized data
  transport. These are remaining implementation work, not completed fake proofs.
- W3/W8/W9 must finish source consumption, runner identity and final clients.
  The dashboard's token list and machine enrollment still use their legacy paths.
- Native HTTP/api.sock, Windows/Darwin, PostgreSQL, physical storage and real-repo
  QA remain with T1/lead. No new product-policy question was introduced.

## Verification

Go used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and GOCACHE,
GOTMPDIR and TMPDIR inside /tmp/sw-astra-W2. Tests used -p 4 at most.

| Check | Result |
| --- | --- |
| Whole-repository go build -p 4 ./... | Pass after merge resolution and implementation. |
| Scoped vet on capability, apierr, controller, acceptance, apiroutes, apispec and apiref | Pass. |
| Complete capability/API-error/executable acceptance/route/reference suites | Pass. Native pending cells remain skipped. |
| Credential issuance HTTP and real-store tests | Pass. |
| Plan-secret opener/property tests and valid plan acceptance | Pass. |
| Issuance authority fuzz, five seconds/two workers | Pass, 2,203 executions. |
| Exact controller-route classification and duplicate inventory | Pass. |
| API/schema generation, deterministic spec and check-api-spec.sh | Pass. |
| Controller public API snapshot | Regenerated and matches. Other snapshots were not rewritten. |
| Frontend teams caller tests | Pass, 42 tests, zero skips. |
| Frontend tsc --noEmit --incremental false | Pass. |
| Embedded source-doc/changelog mirrors | Pass after sync. |
| commentcheck -base main | Pass, clean across 1,006 files. |
| git diff --check | Pass. |

The broader controller selector still fails the existing cancellation-reason test
and eight legacy TestSourceCredential cases that create rows in removed tokens.
The full docs symbol check fails on 17 removed SDK symbols in unchanged W9 docs.
These failures are retained. No Sparkwing CLI, pipeline, listener or git write ran.

The frontend test script could not spawn its child Node process in this sandbox.
The same teams tests pass directly with the command below. The original run
used the default tsx cache. Its 74 session-created entries for this worktree
were removed while the six older entries were preserved. Reproduction keeps
all temporary state inside the W2 scratch directory:

```sh
env -C /home/bombuntu/.xwing/envs/1080/sparkwing/web TMPDIR=/tmp/sw-astra-W2/tmp TSX_DISABLE_CACHE=1 node --import tsx --test --experimental-test-isolation=none src/lib/teams.test.ts
```

Reproduce the focused Go evidence with the environment above and:

```sh
go test -p 4 ./internal/controller ./tests/acceptance -run '^TestCredentialIssuance|^FuzzCredentialIssuance|^TestPlanSecretOpener|^FuzzPlanSecret' -count=1
go test -p 4 ./internal/capability ./internal/apierr ./internal/acceptance ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go run ./internal/commentcheck -base main .
```

Shared edits outside W2: internal/acceptance/credential_routes.go,
tests/acceptance/credential_issuance_http_test.go, web/src/lib/teams.ts,
web/src/lib/teams.test.ts and web/src/app/team/machines/page.tsx. The merge also
resolves internal/controllerwire/contracts.go. Generated/shared artifacts are
api/openapi.yaml, docs/api-reference.md, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md, their mirrors and
.apidiff/pkg_controller.txt. No production Go caller outside W2 needed migration.

Friction remains the session/CSRF and App permit contracts, cancellation
intent/result conflation, old credential fixtures and W9 documentation drift.
Scratch is removed after final checks. Git identity and core.worktree were not
changed. The lead owns staging, committing, independent review and gates.

<!-- design:end -->
