# W2 secret-read continuation

<!-- design:begin -->

The sole incoming conflict was in contracts/handoffs.md. Both the W2 evidence
and incoming W1b/W8 design handoffs survive. No content conflict marker remains.
The index stays unmerged because the swarm brief forbids Git writes. The lead
must stage the resolved file. HEAD was 5f4dfde6f when this continuation began.

## Implemented

GET /api/v1/secrets/{name} now uses PolicyRouter, the frozen route alternatives
and the common verifier. The authenticated team selects its reader. Decoding
checks name grammar, malformed queries and duplicate run/pipeline selectors
after authentication. HEAD is refused. Success and errors use no-store.

The local C2 SecretReader interface takes Authorized and returns a stored row.
It owns transactional authority, sealed declaration/pipeline/shared/pool checks
and the claim's release audit. The controller passes the complete principal,
binding, selected alternative and selectors. It rejects missing or foreign-team
adapters. No controller SQL or retired ClaimToken fallback is added. W1 has not
implemented the interface, so the default production adapter refuses with
policy_missing. The real-tenant refusal is tested. Fakes prove the HTTP boundary,
not SQL fencing or audit atomicity.

Invariant: a masked value reaches only a live, declaring claim with the exact
sealed secret scope. Members and verified peers read unmasked config within
their authenticated tenant. The response rejects wrong-row and masked-member
results before decryption. It uses the complete row binding and omits cipher
diagnostics on failure. Cancellation, stale holders, ended leases, revoked
ancestry, missing grants and foreign run selectors cannot reach decryption.

Invariant: no row-secret helper can invoke unbound Seal or Open. A generated
property test failed against the former fallback. Shared helpers now require
BoundCipher for reads and writes, including plan-secret, git and webhook callers.
The unbound cipher is never called. Mutations with an unbound cipher return
unavailable and persist no rows. Existing bound local-keyring tests pass.

Startup resealing and LegacyCipher are removed. Plaintext and pre-team migration
code no longer upgrades stored values. Explicit tenant key rotation remains.
Three callers were migrated by deleting the removed method or interface use.
The W6-owned OpenLegacy implementations remain outside this controller cut.

## Deletions and ports

Removed 124 lines of startup resealing and 278 lines of its old tests.
Removed 142 lines of legacy secret-read handlers and the unused claim-kind
list. This is 544 lines before the cipher cleanup and caller removals.
Cipher cleanup removes another 70 lines, including the legacy interface,
legacy opener, unbound fallbacks and stale comments, and adds 17 lines.

The moved-envelope regression is preserved in TestSecretReadHTTPBindingProperty.
It generates envelope moves across teams and changes to name, pipeline, shared
and masked fields. It also generates plaintext/unbound bytes and private errors.
TestSecretRowHelpersNeverUseUnboundCipherProperty checks the common helper
boundary. TestSecretReadHTTPAuthorityProperty generates authority failures.
These are component ports using real encryption and fake persistence. No parked
T1 SQL/native port or new native matrix cell is claimed complete. The three
resealing tests describe the removed migration and are retired.

## Amendment and handoff dispositions

Earlier completed work remains in W2-sessions-cancellation-handoff.md and its
linked handoffs. No completed implementation was rebuilt here.

| Rows naming W2 | Disposition and remaining owner |
| --- | --- |
| A1-01/05 | W2's shared mask/annotation/summary policies remain. W3 owns production service registration, process facts and acknowledged sinks. |
| A1-03 | DecodeInner remains implemented and its package tests pass. |
| A1-08/09/19/26/55 | W2's final runner/launcher/named claim policies and endpoints remain. W1/W8 enrollment/software-policy adapters and W9 clients/identity delivery remain integration work. |
| A1-11 | The sealed Source.Location contract remains. W1/W3/W6/W8/W9 own trusted acquisition and execution. |
| A1-14/15 | Failure/cancellation adapters remain. W1 still omits BindingState.CancelReason from ReadAuthentication. Its existing job-intent precedence handoff remains open. |
| A1-16 | W2's private execution/expansion projection remains. W8/W9 must deliver real private awards. |
| A1-20/21/22/23 | Secret reads join shared policy and the legacy cipher/startup cuts are done. W2 still owns the remaining route/authentication cuts, root provisioning, roleless browser flows and console/peer adapters. W1's snapshot lookup accepts only capability and account-session classes. W3 owns real node-host registration. |
| A1-24/25 | Existing bound data/source adapters and plan-secret opener remain. Row helpers now refuse unbound ciphers. W1 owns live transactional reads, including the new SecretReader and prior credential mutation/maintenance adapters. |
| A1-27/28 | Existing provenance/payment verification remains. W1/W1b own real award/start/settlement integration. Payment configuration and the existing card/dispute event-contract question remain open. |
| A1-29/30/31/32/33/34/35 | Existing signed data/records/cache transports remain. W1/W7/W8 own live authority/metadata/charging, backend wiring and logical selection. W9 data clients remain stubs. |
| A1-38 | Bootstrap exchange/renewal and fake-backed launcher sync remain. W1 still owes the real sync projection. |
| A1-39/40/49 | W2 endpoints remain available through their contracts. W8/W9 own real runner/local-controller/private-file consumers. LocalSubject and LocalRuntime lifecycle operations are not completed by this worker. |
| A1-53 | Native HTTP/api.sock/placements/backends remain T1/lead work. No native cell was unskipped. |

The W1/W2 session/cancellation handoff remains partly open. Session matching and
default CSRF were completed previously. ReadAuthentication still selects only
cancellation timestamps, not reasons. No W1 file was changed. The W4 service-router,
execution-identity, eval, expansion, terminal-delivery and pipeline-identity design
handoffs have no new W2 implementation request. Their named owners retain them.

There is no foreign contract signature change. SecretReader is declared in C2.
W1 must implement it on Tenant. Existing C2 CredentialWriter, CredentialRotator
and SourceAuditReader are also still absent on Tenant. W9's NewEngineClient and
NewDataClient still panic. These findings were checked against the merged code.

## Shared files

Removed-API caller migrations are cmd/sparkwing-controller/main.go,
internal/orchestrator/wingd_api.go and internal/localsecrets/cipher.go. Their only
behavioral changes are deleting startup migration calls and a legacy interface
assertion. No foreign implementation was substituted.

Other shared edits are contracts/c2-credentials.md, contracts/handoffs.md,
tests/acceptance/secret_reads_http_test.go,
tests/acceptance/credential_mutations_http_test.go, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md, api/openapi.yaml,
docs/api-reference.md, perf/budgets.json, their generated documentation mirrors
and .apidiff/pkg_controller.txt. The new controller implementation and helper
property test are W2-owned.

## Verification

All final commands passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4, GOCACHE=/tmp/sw-astra-W2/cache and
GOTMPDIR=TMPDIR=/tmp/sw-astra-W2/tmp. Each Go test used -p 4.

- Whole-repository go build -p 4 ./... passed.
- Scoped go vet passed for capability, apierr, controller, internal/acceptance,
  tests/acceptance, sparkwing-controller, orchestrator and localsecrets.
- Complete capability, apierr and localsecrets tests passed. Ciphertest compiled
  and has no standalone tests. API route, specification and reference tests pass.
- The W2 acceptance selection passed in 23.665 seconds. It selects TestCredential,
  SecretRead, SourceRelease, SourceConsent, Authority, Operator, Deployment, Data,
  Engine, Payment, Credit, Sizing, Discovery, Identity and Queue, plus the matching
  credential/data/engine/sizing/source fuzz seed prefixes. TestCredentials is
  included, rerunning credentials-session-digest-csrf/store-sqlite and
  credentials-operator-console-split/store-sqlite. Native pending cells stay skipped.
- New TestSecretReadHTTPUsesPolicyAndCommittedRow, RefusalsNeverOpenValues,
  RequiresTransactionalReader, BindingProperty and AuthorityProperty passed.
  The two TestSecretRowHelpers tests pass. Credential mutation cipher-refusal
  coverage now includes an unbound implementation and checks no row was stored.
- Focused controller policy, persisted-session/CSRF and plan-secret acceptance
  tests pass. Orchestrator route inventory and documentation mirror tests pass.
- go run ./internal/commentcheck -base main . passed across 1,161 files.
  Documentation/changelog mirrors, the controller API snapshot and
  git diff --check pass.

The first new helper property failed on the unbound fallback before the fix.
The first API specification run failed on the old GET secret scope text.
The generator corrected that registration-derived text. Its prose check also
refused repeated scope names, so the description now leaves those names to the
generated scope field. The final specification and reference tests pass.

No full legacy-controller, performance, socket, PostgreSQL or native placement
pass is claimed. The TypeScript compiler file remains absent under node_modules,
so native TypeScript evidence is still unavailable. The prior unrelated broad
docs failures were not rerun. No Sparkwing binary or product pipeline ran,
because the swarm brief reserves those checks for the lead.

## Friction and continuation

The merge index remains read-only and unmerged despite resolved contents.
No Git write ran and HEAD remains 5f4dfde6f. The default Git identity remains
Korey's. The new SecretReader and the prior credential writer/maintenance adapters
need W1 implementation before real integration can pass. W2 still owns the
remaining legacy route/authentication cuts, provisioning and browser/console/peer
adapters. No foreign contract was patched around a stub.

All owned check processes completed. /tmp/sw-astra-W2 is deleted after the final
content, mirror and API checks. The lead retains staging, independent review,
product pipelines and landing. Engineering completion is not claimed.

STATUS: CONTINUE

<!-- design:end -->
