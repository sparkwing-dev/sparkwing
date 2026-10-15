# W2 OIDC hard cut and integration merge

<!-- design:begin -->

## Result

All five merge conflict contents are resolved. The lead must stage them and
finish the merge because the swarm brief forbids Git writes. No conflict
marker remains. amendments-1.md and handoffs.md match MERGE_HEAD byte for byte.

W2's account reader/writer and policy routes survive. Integration's removed
artifact-store API, cache-grant routes and gitcache routes stay removed.
OpenAPI retains the owner heartbeat and new signed-data routes. The tenant
catalog does not regain migrated account or credit registrations. API snapshots,
API references and embedded documentation are regenerated.

POST /api/v1/runs/{run}/oidc-token now uses the declared
OIDCMint/BindRun/LiveSensitive policy with RetryNever. A machine root, account
credential, bootstrap or unrelated live claim cannot mint. A missing/dead
credential answers 401. A live credential missing authority answers 403.
Malformed requests and invalid subject values answer 400. Typed transaction
refusals propagate through the shared envelope. Private diagnostics do not.

The body carries only audience. A tenant reader supplies committed run facts
and a complete ancestry expiry ceiling. The HTTP adapter checks returned
team/run, bounds expiry again by its authenticated leaf and binding, and uses
the existing RS256 signer. The default ten-minute TTL, existing per-claim
30-token budget and five-minute refill remain. Budget state is replica-local.
Key publication/rotation and the existing client retain their request/response
shapes. Future revocation deadlines also cap the issued JWT. Already issued
external tokens retain the accepted expiry residual.

## Deleted

- The 180-line legacy OIDC mint/projection section, including ClaimedRun,
  token-Principal, Trigger and ambient-clock lookups, is replaced.
- The six-line dispatched-claim OIDC refusal stub is deleted, plus its
  separating blank line.
- The old mux registration and tenant-catalog entry are deleted. The legacy
  runner's permitted-write list also loses its OIDC entry.
- The 639-line legacy OIDC fixture and twelve tests are deleted after their
  HTTP/signing requirements were ported. The six-line obsolete OIDC-gap
  assertion in claim_run_test.go is deleted and reversed by successful minting.
- oidc_token.go shrinks from 257 to 144 lines. The shared fact type adds 18.

## Assumptions and contract changes needed

New local C2 mechanism: OIDCReader and WithOIDCReader, returning
capability.OIDCFacts from ReadOIDCFacts(ctx, Authorized). The resolver and
reader both retain tenant ownership. The smaller alternative, using ReadRun
or the legacy ClaimedRun/Trigger readers, cannot supply a committing authority
recheck, the complete ancestry ceiling and verified original event/provenance
facts. Undoing the seam means reverting this OIDC cutover. No foreign signature,
schema, clock, trust root, limit, environment variable or compatibility path is
changed.

W1 must implement this C2 reader on Tenant. Use one store-clock transaction to
recheck the live ancestry, exact run/tenant, oidc.mint grant, holder/generation,
cancellation and operational unit. Derive pipeline, exact SHA, ref, repository
and event from immutable intake/accepted facts, retaining retry/child lineage.
Only verified App events yield push/PR/release/create/delete and repository ID.
PR and tag refs must preserve their distinct subjects. Only controller schedule
provenance yields cron. Derive runner_kind from persisted enrollment/award
provenance, preserving runner/github-actions/user/service spellings. NotAfter
must include every ancestor expiry, future revocation and binding deadline.

The complete contract is in c2-credentials.md. The default refuses a missing
reader with policy_missing. Tests use a committed-fact fake, not legacy SQL.
No real C1 identity-selection or mint-race coverage is claimed. There is no
new architecture question for the human in this slice.

## Evidence and ports

Invariant: an OIDC JWT names only the authenticated live claim's sealed run
facts and cannot outlive that claim's effective authority.

The six TestOIDCMintHTTP* tests exercise actual Server registrations, the real
verifier/policy, signer, JWT/JWKS verification and existing client. Generated
selectors cannot replace identity. Generated expiry, revocation and binding
ceilings bound JWT exp. Generated separator/wildcard/whitespace/control/Unicode
subject values release no JWT or private field value. Refusals cover ancestry,
expiry, cancellation, grant/binding, credential classes, reader failures and
transaction races. Other cases cover all retained subject/event/ref spellings,
repository separation, bad audiences, unsupported methods, disabled signing,
per-claim budget isolation, distinct JWTs, key rotation and tampering.

The twelve old controller tests map to these tests and the existing
internal/oidcissuer suite and TestOIDCDiscoveryHTTP* tests. The port inventory
records the deleted dispatched-claim-gap assertion and the remaining C1 facts
projection obligations. No native credential matrix cell is newly completed.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp,
with go -C pointing to the assigned worktree and tests using -p 4.

- Whole-repository go build -p 4 ./... passes on the final product source.
- Scoped vet passes for controller, controller/client, capability, apierr and
  tests/acceptance.
- The broad W2 executable acceptance selection passes in 12.397 seconds:
  ^Test(Account|Authority|Credential|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine).
- Final ^TestOIDC selection passes in 0.916 seconds, including the later
  subject-injection property and transaction-refusal cases.
- Controller Policy, AuthorizedHandlers, RouteGuard, TenantRoute and SessionRoute
  checks pass. Full capability, apierr, oidcissuer, apiroutes, apiref and apispec
  package tests pass.
- commentcheck against main, API spec/snapshot drift, documentation sync,
  changelog and whitespace checks pass.

No Sparkwing binary or socket-based pipeline is run. Native TypeScript was
already green in the preceding session and has no changed input here, so it
was not rerun. There is no TypeScript setup error to report. The lead owns full
pipelines, independent review and native integration before landing.

## Amendment and handoff continuation

| Assignments | Current disposition |
| --- | --- |
| A1-01/03/05 | Existing process-service policy and canonical decoder retained. Actual host composition remains W3. |
| A1-08/09/19/26/38/55 | Existing claim/enrollment/private-award/exchange/here and sync adapters retained. W8's old SyncLauncher-stub wording is stale: W2 has the implemented reader adapter. C1 enrollment and production sync facts remain required. |
| A1-11/14/15/16/17 | Existing sealed source, failure, cancellation, snapshot and lifecycle adapters retained. W9/W3 still need captured SourceAttempt transport. |
| A1-20/49 | Root issuer retained. Mounted bootstrap raw-credential delivery and production origin/enrollment persistence remain joint work. |
| A1-21/22/23 | OIDC now leaves the legacy credential router. Earlier console proof projection and transport-only peer identity boundary remain implemented. C1 peer authority and authority-bearing console writes remain open. |
| A1-24/25 | Source/data transaction and plan-secret seams retained. Production adapters remain with their owners. |
| A1-27 | Existing shared billing calculator retained. |
| A1-28 | Verified committed-checkout settlement retained. Card/refund/warning/dispute event contracts remain joint C1/C2 work. |
| A1-29/30/31/32/33/34/35 | Existing authenticated data/record/cache adapters retained with integration's object-backend cut. Production authority/metadata/read-set/quota composition still needs integrated evidence. |
| A1-39/40/53 | Native runner/local-controller composition and placement evidence remain required with their owners and T1/lead. |
| W1 cancellation snapshot | BindingState.CancelReason and W2's response consumer already exist. authentication_binding.go still selects cancellation timestamps without the reason. No W1 projection is claimed landed; job intent must precede run intent. |
| W9/W2 step reports | Existing C1 RecordStep adapter retained. Typed wire and captured execution ordinal remain W9/W3 integration. |

Priority 1 remains incomplete. Legacy execution/reporting, concurrency,
team/GitHub App/billing routes and their auth.go/claim_route.go middleware still
need migration and deletion. Identity-link completion still needs C1's durable
nonce and roleless account-session representation. Other pending C1 adapters
from earlier W2 handoffs remain required: account reads/mutations, console
writes/deletion, source/secret mutations and maintenance, root management,
sizing, data and provider-event integration. This slice adds the OIDC facts
reader to that list. No blocker is represented as completed work.

## Shared files and friction

The only client caller touched is pkg/controller/client/oidc.go. Its documentation
now states the final authorization rule; its existing wire shape already fits
and passes the real client/handler test. No caller outside controller needs a
production source change in this slice.

Shared surfaces touched: api/openapi.yaml, .apidiff/pkg_controller.txt,
docs/oidc.md, docs/auth.md, docs/api-reference.md, docs/migrations/_unreleased.md,
CHANGELOG.md and their generated pkg/docs mirrors. The contract port inventory
and credential-route appendix record the cut and its evidence. All implementation
changes are W2-owned controller/capability files and acceptance tests.

Friction: the merge index remains unmerged until the lead stages the five
resolved files. The C1 OIDC reader is not implemented. The expanded property
initially used a bare space as a private-value sentinel, which matched ordinary
error-message spacing; the generator now gives private values a distinct prefix.
The handler already refused that input. All stated checks pass after correction.
Scratch is removed at the end of the session.

<!-- design:end -->

STATUS: CONTINUE
