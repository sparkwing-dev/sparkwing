# Credential matrix route port appendix

<!-- design:begin -->

This design appendix enumerates all 229 distinct routes in `pkg/controller/testdata/credential_matrix.golden` at `1e16668363ecbd99ba922436ab1a6f9cae63335b`. Those routes generated 8,466 old cells. Every old route has one row below. This is a complete source-route inventory, not a complete final permission specification.

The frozen architecture determines some removals, resource names and permission boundaries. It does not determine every existing route's role grants. Unresolved rights below are launch blockers for their matrix cells. An unavailable or undecided expected answer cannot be reported as a passing or skipped launch check.

C2 owns the production route policy. T1 owns independent expected results and cross-area assumptions. This appendix neither registers routes nor supplies a second runtime policy table. A proposed resource does not promise that an old operation suffix survives. Where the frozen text names only a resource, the row leaves the exact operation path with C2.

## Final route permission coverage

`internal/acceptance/credential_routes.go` contains committed literal expectations
for all declared final C2 method/path pairs, including the eleven node-host
routes and every public, browser and external-proof route. Each alternative pins
its credential class, complete grant conjunction, binding and liveness together.
Audience, resource owner and authentication proof are pinned per route. Empty
grants are explicit, including account and discovery alternatives. No expected
value is obtained from `capability.Routes`, its constants, or observed responses
when the acceptance test runs.

[Amendments 1](amendments-1.md) supplies the decisions for masks (A1-01),
annotations and summaries (A1-05), unified runner claims (A1-08), failure reads
(A1-14) and job cancellation (A1-15). The literals include those routes and
remove the separate runner job/run claim routes. A mixed-kind claim still
requires every requested kind's grant through claimSelection.

The cache transport adds POST `/api/v1/data/memo/lookup`,
`/api/v1/data/depcache/lookup` and `/api/v1/data/memo/copy`. Lookups retain the
data-read alternatives. Copy requires the conjunction `data.read,data.write`
on one sensitive claim alternative. Its destination is the bound attempt.
HTTP component proofs exercise these registrations. Native placement cells
remain separate.

`TestFinalCredentialRouteDeclarations` compares the expected inventory with
`capability.Routes`. Missing, extra and duplicate routes fail. Missing, extra and duplicate
alternatives or grants fail, as do changed credential, binding, liveness,
audience, owner or authentication proof. Alternative order has no meaning.
Grant/liveness pairs are compared as complete tuples; independent grant and
liveness unions would incorrectly let alternatives lend authority to each other.
`TestFinalCredentialRouteCoverageRejectsDrift` demonstrates these failures on
private copies, including grant swaps and liveness swaps that preserve each
route's aggregate sets. Production tables are never mutated by these tests.

These are declaration coverage tests. The rewritten router is not active and
`Authorize` is a stub. Passing them proves neither runtime registration coverage
nor authorization behavior. Activation requires a router-owned inventory of
actual controller and node-host registrations, compared against these same
literal keys and passed to `ValidateRoutes`. That inventory must come from the
router, not from policy rows or expectations. The legacy 229-route matrix cannot
satisfy this requirement. The behavioral credential matrix must exercise every
complete alternative separately, including each alternative's positive case,
missing grant, wrong binding, wrong audience and liveness refusal, and attempt
to combine one alternative's grants with another's liveness. A passing
policy-shape check cannot replace those HTTP/socket observations.

### Authority and provisional choices

The literals preserve the declared C2 surface for drift detection. Transcribing
a declaration does not independently establish that its permission is correct.
The rules below supply the independent security boundaries; exact role and path
choices beyond those boundaries remain the named assumptions until reconciled.
Changes must be reviewed against these sources and recorded expectations, never
accepted by regenerating a golden file from C2 or a running controller.

| Expected permission family | Independent authority | Remaining assumption |
|---|---|---|
| Awarded run/job reads, writes, results, heartbeat, source, OIDC, child, sizing and slot operations | Frozen §5.1/A68: exact grants, audience, tenancy and bound relationships; §3.3 transactional holder fences | Exact operation suffixes and trigger/plan grant mappings are C2's declared choices; `U-REPORT`, `U-PLAN`, `U-CLAIM-CONTROL`, `U-CONTROL` require reconciliation. |
| Data reads and publication | Frozen §5.1/A66, §8.1; decisions 92–93 and 98: controller authority, read sets, repo-bound read-only cache and local-run publication | C1 supplies receipt/read-set facts. A route tuple alone cannot establish object authority. |
| Expansion reads/submission and eval heartbeat/logs | Frozen §3.6 and C2's route and liveness invariants: expansion.submit and bound source-output read with owner/eval lease; eval reporting requires its own eval lease | Eval failures use AcceptExpansion. Runtime lease and source-generation checks remain required. |
| Operator and deployment administration | Frozen §9.3 and §5.1: console-only trust, grants, freeze, refunds and account deletion; deployment operational scopes only | Exact operational permission spellings remain `U-DEPLOYMENT`; console query choices remain `U-OPERATOR-READ`. |
| Member, account, token-owner, machine-owner and team administration | Frozen §5.5: account role in its team; §5.1: no claim privilege from a root | Exact member grants and root provisioning rights are not specified by the frozen text. The C2 choices pinned here remain the `U-*` role assumptions below. |
| Public, browser, signed-object, GitHub webhook, Actions exchange and payment webhook | Frozen §§3.5, 5.1, 5.5, 8.1; decision 98; C2's billing contract: webhook-derived trust, exact signed-object scope, separate Actions proof and signed Stripe settlement | Browser endpoint paths and account-flow choices remain C2 assumptions. Payment proof and settlement still require runtime evidence. |
| Node-host process operations | Frozen §§5.1, 5.5, 6.2–6.3: nodehost audience, process lifetime and declared operation bindings | C3 must expose actual node-host registrations; no controller credential or process-env bearer fallback is permitted. |

### Credential creation correction

`POST /api/v1/tokens` requires member `runs.read` with `none` binding.
Creation has no existing token selector. The issuer derives the member subject
and team from live authority and checks grants in the committing transaction.
Its one-time bearer makes automatic retries unsafe, so retry policy is `never`.

The Actions exchange at `POST /api/v1/runners/github/exchange` also has retry
policy `never` because it returns a new bearer without secret replay. Its proof
is the signed Actions JWT plus the selected team's matching repository/owner
binding. Caller-selected grants, push facts, pools and metering are refused.
Token-specific lookup, revocation and rotation keep their own-token binding.

### Reconciled C2 permissions

Capability inventory uses member `runs.read` with `none` for the collection.
The persistence read filters owned credentials or the current team admin's
tenant. A collection has no existing token selector. Detail and rotation keep
`ownToken`. Rotation returns a one-time bearer and has retry policy `never`.
The component proofs are `TestCapabilityManagementHTTP*`. Native matrix
activation still needs the real persistence adapter and transports.

C2's [route and liveness invariants](c2-credentials.md#route-and-liveness-invariants)
and [billing contract](c2-credentials.md#billing) settle these expectations.
The literals transcribe those documented responsibilities against frozen
§§3.3, 3.6 and 5.1. They are written independently of the code table.

| Route | Expected permission | Reason |
|---|---|---|
| `GET /api/v1/runs/{run}/snapshot` | Controller audience, run owner, capability proof; awarded/delegated claim with `trigger.host`, `runOfTrigger`, sensitive liveness. | The remote run owner reads the state used for reconciliation under its live trigger authority. |
| `POST /api/v1/runs/{run}/jobs/{job}/reconcile` | Controller audience, run owner, capability proof; awarded/delegated claim with `nodes.claim`, `runOfTrigger`, sensitive liveness. | Reconciliation may self-claim a job in the owner's run. C1 checks holder rights, the proposed source's read edge and persisted outcome in the transaction. |
| `POST /webhooks/stripe` | Controller audience, team owner, `payment_webhook_signature` proof; no capability alternative. | Automatic settlement requires the signed body and Stripe-Signature header, timestamp and configured mode. The committed checkout determines the team and credit amount. A bearer or browser return cannot settle credit. |

The four controller routes `POST /api/v1/claims/{claim}/heartbeat`,
`POST /api/v1/claims/{claim}/logs`,
`POST /api/v1/claims/{claim}/log-segments` and
`POST /api/v1/claims/{claim}/log-manifest` each admit the complete alternative
`eval_unit|expansion.submit|ownEval|expansion`, with run ownership and capability
proof. The heartbeat also keeps its claim `node.report`/`ownClaim`/reporting
alternative. Each log route keeps its claim `log.tail`/`ownClaim`/logTail
alternative.

Eval authority carries exactly `expansion.submit` and `data.read`, so reporting
uses the expansion grant under its own live eval lease. `ownEval` binds the path
claim and unit log subject to the eval's run, holder and generation, and checks
the immutable source job and source generation. A sibling eval, bootstrap or
owner-delegated expansion capability cannot satisfy this alternative. Logs
must arrive before lease-ending completion. Structured eval failure uses
`/jobs/{job}/expansion` and the same AcceptExpansion transaction as success.

Root assumption: the stable comparison seam is `capability.RoutePolicy` and its
`AccessRule` tuple, not `ProvisionSpec`, `AuthorityRef`, the capability parent FK
or the unresolved root issuer. T1 imports those route declarations only on the
actual side of the test. C1/C2 must settle session ancestry and provisioning
before runtime member/machine/service root cells can pass. Undoing this assumption
requires updating the test adapter, not regenerating expected permissions.

Decisions 101–103 replace the old N/N−1 version promise with fetched engines,
a signed version index and a 12-month controller-API boundary. These expectations
cover that controller boundary; they do not invent a controller engine-download
route because decision 103 distributes engines through GitHub Releases. Changes
to final controller registrations require explicit coverage changes. Decision 104
also keeps the same permission contract for TypeScript on Windows.

The remaining unresolved rights and historical port inventory below still
require reconciliation. The expectations above settle the payment callback
and eval heartbeat/log declarations. Runtime authorization and settlement
remain acceptance obligations beyond this declaration guard.

## How to read each row

- `keep` means the frozen resource preserves the shown surface. It does not settle an unresolved permission rule in the last column.
- `rename` means the frozen vocabulary or resource moves. Operation suffixes left to C2 are not guessed here.
- `delete` means the old route must disappear without a compatibility alias. Its surviving concern moves to the stated resource or internal operation.
- `unresolved` means the frozen text does not determine the old route's final wire disposition. Any known binding still constrains its replacement.

Each row names an existing scenario ID. All surviving authenticated operations also run the common `AUTH` cases in `credentials-matrix-authentication-status`, even when the row names a more specific primary scenario. Deleted routes use `surface-api-and-detail-parity` to prove absence. Replacements also inherit the named permission cases once C2 specifies their paths.

## Independent permission rules

These rules come from the frozen text. They are expected outcomes, not observations of the production policy. A rule specifies only the authority stated here. It cannot fill an unresolved role or grant by inference from a route's old name.

No bound rule has a blanket admin bypass. A separately authorized operator action is a distinct contract, not permission to ignore `ownNode`, `runOfTrigger`, `bodyNamesRun`, `pipelineOfBoundRun`, `childOfBoundNode` or `slotOfBoundNode`. Audience checks also apply before a handler runs.

| Rule | Independent expected result and source |
|---|---|
| `AUTH` | Route match precedes authentication, with route liveness included. Absent, unknown, expired, revoked or ended-claim credentials return 401 `unauthenticated`. A live credential with the wrong audience, missing grant or wrong binding returns 403 `forbidden` with `details.reason`. A transaction recheck that loses its fence returns 409 `lock_held` or `illegal_transition`. An authenticated route with no policy fails closed with 500 and fails coverage. Frozen §5.1/A68 and §6.5. |
| `RUN` | Run binding confines execution authority to the bound run and team. Account access still requires its separately specified team role. A run ID in input grants no authority. Frozen §5.1 and §5.5. |
| `OWN` | `ownNode` requires the bound job, including the job named in the `claim/input` path. A different job in the same run is not equivalent. The award must be live and have the operation's grant. Frozen §5.1/A68. |
| `HOST` | `runOfTrigger` reaches the owner's own run for host-side operations. It does not let the owner overwrite another live node holder. The store rechecks holder, generation and its own clock. A foreign run fails binding. A race after authentication returns 409. Frozen §5.1/A68 and §3.3. |
| `BODY` | `bodyNamesRun` checks the run named by creation/parent bodies. Substituting another parent or run fails authorization before creation. Here intake requires the requester's live `runs.write` in the run's team. Ordinary role-to-intake grants remain `U-RUN`. Frozen §5.1/A68. |
| `PIPE` | `pipelineOfBoundRun` limits a work claim to its bound run's exact repo/pipeline sizing identity. A machine root with claim scope alone cannot read arbitrary pipeline sizing. A bare or encoded name cannot manufacture another repo's binding. Frozen §5.1/A68, §4.1 repo identity and §9.5. |
| `CHILD` | `children.start` and `childOfBoundNode` restrict child creation/read to the bound parent job's child. A sibling job's or foreign run's child is denied. Reading a child's output also requires the declared-reference/read-set rule. Frozen §5.1, §5.5 and §8.1. |
| `SLOT` | `slotOfBoundNode` checks the bound job and slot holder names. Neither an admin credential nor tokenless local mode bypasses that bound-route check. A separate administrative force-release operation needs independently specified authority, recorded as `U-QUEUE`. Frozen §5.1/A68. |
| `DATA` | `data.read`/`data.write` are bound to run, repo and server-derived ref scope. Reads and `HasMany` are limited to the read set. Reference creation requires own verified upload receipt or an already authorized dependency/reference/memo edge. Foreign existing bytes and unrelated same-run job receipts are insufficient. `CommitRecord` checks every member. Manual refs cannot overwrite verified refs. Frozen §5.1 and §8.1/A66. |
| `LOG` | A bound claim can use its permitted job log-tail/report operations. Trigger owners remain subject to holder rights. Log reads reauthorize through the controller. Signed log PUT URLs name only their segment/manifest and last 15 minutes, without early revocation. Live authority for one job cannot sign or seal another holder's log. Account log-read roles remain `U-RUN`. Frozen §5.1, §5.5 and §8.1. |
| `SECRET` | Execution release requires a live `secrets.release` grant, declared secrets and the awarded binding. `only_on` also requires the awarded reserved pool. Restricted secrets are refused on here machines. A machine root with claim scopes alone cannot read execution secrets. Account secret CRUD is `U-SECRET-ADMIN`. Frozen §3.5, §5.1, §5.3 and §5.5. |
| `SOURCE` | A live `source.read` grant is necessary. Team git credentials additionally require a team cloud runner or an owner-opted-in machine, and every release is audited. Source binding never follows a typed repo string alone. Management roles are `U-GIT-ADMIN`. Frozen §2.5, §5.1 and §5.5. |
| `OIDC` | A live awarded `oidc.mint` and the correct binding authorize minting. A machine root with claim scopes alone does not. Revocation stops new mint requests, not already issued external credentials. Frozen §5.1 and §5.5. |
| `AWARD` | `nodes.claim`, `triggers.claim`, `claims.launch` or `trigger.host` authorizes the applicable claim transaction. Team, unit and fence come from the awarded row. Only the launcher's service authority awards across teams. Award grants come from the fixed unit table, not supplied request grants. Claim matching and holder rights still apply. Frozen §3.5 and §5.1/A58. |
| `ISSUE` | Delegate narrows team, grants, binding and expiry. Award is privileged and derives authority from the row. Exchange copies the bootstrap binding, revokes the bootstrap and parents the work capability to the claimant root. None of these rules grants a user permission to create another user's root credential. Root-management roles remain `U-TOKEN`. Frozen §5.1/A58. |
| `NODE-WRITE` | Node reports/results require the awarded holder and appropriate fixed grant. Terminal state is absorbing. Exact repeated payloads can replay, while stale or competing writes fail. Lifecycle events commit with transitions, not through host-created duplicates. Frozen §3.3 and §5.1. |
| `PLAN` | AcceptPlan checks the live planning/owner authority, seals spec and plan in one transaction and refuses drift. It cannot be invoked by an unrelated job claim. Fixed plan-unit grant mapping must be confirmed by C2 independently of runtime policy. Frozen §3.1, §3.8 and §5.1. |
| `EXPANSION` | Only the owner-delegated expansion capability or an awarded eval unit submits. Check live owner/eval lease, immutable source generation, expected expansion and no cancellation in the transaction. A leftover terminal source claim is insufficient. Frozen §3.6. |
| `PUSH` | Actions exchange is confined to its verified push scope. It does not establish App branch trust. HMAC-verified App facts and replay-key dedup establish that trust, with exact-SHA checkout and retained trust on retry. Frozen §3.5, §5.5 and decision 98. |
| `OPS` | Trust changes, grants, freezes, refunds and account deletion require an operator-console session. Deployment-admin tokens only manage operational ceilings/signup settings, deployment object storage and deployment service credentials. They cannot obtain console powers. Break-glass is an in-pod controller command, not an added public route. Frozen §5.1 and §9.3. |
| `BILLING-PROVENANCE` | Metered status is operator-controlled on the root and copied immutably into awards. Editing a current token cannot rewrite an existing award's billing authority. This does not identify which root-administration route may edit it. Frozen §4.2 and A59. |
| `CUT-CACHE` | Delete cache server, gitcache proxy, cache audience, `swcg` grants and storage-count scope. Object signing uses controller capability authorization. Frozen §5.1, §8.4 and §9.3. |
| `CUT-SURFACE` | Delete bounce, dispatch history, raw node status, auto-retry reset, usage-metrics and repo-dispatch surfaces. Frozen §3.4 and §9.3-9.5. |
| `CUT-TRIGGER` | Delete customer `/triggers*`. Intake/list/detail become run resources. Owner claims still exist internally and need C2-chosen paths. Frozen §3.1, §5.1 and §9.5. |
| `CUT-HEARTBEAT` | Controller-backed owner liveness uses the owner lease. Remove the separate run-row heartbeat as an authority. C2 decides whether the wire operation is removed or renews the one owner lease instead. Frozen §3.4/A5. |

The common matrix must vary unknown, expired, revoked, terminal-bound, stale-generation, wrong-holder, wrong-audience and revoked-parent presentations. It must exercise a transaction race after successful authentication. Wrong authority is not equivalent to a malformed request. Positive cells require valid minimal bodies, and denial cases check that no mutation or protected disclosure occurred.

## Rights that the frozen text does not supply

Each `U-*` entry blocks completion of its affected matrix cells. C2 must state the intended right with a written source or decision for lead approval. T1 then writes expected outcomes from that independent statement. Reading the implementation's policy row, copying old golden answers, or regenerating observed responses cannot resolve a `U-*` entry.

| Unresolved right | Confirmation required |
|---|---|
| `U-SERVICES` | Whether `GET /api/v1/services` survives, its audience, grant and binding, and specifically whether here and Actions presentations may read it. Frozen §10 L72 flags the disagreement but gives neither answer. These cells are launch blockers. |
| `U-ACCOUNT-DELETE` | Final console deletion path and which account may be selected. Console-only authority is fixed by §9.3. |
| `U-ACCOUNT` | Account self-service, linked identities, active-team selection, team CRUD/deletion, invitations and membership role permissions. §5.5 says account role governs access but does not enumerate these roles or grants. |
| `U-RUN` | Member/session role matrix for run intake, list/detail, deletion/prune, cancel, retry, events, receipts and account log reads. Execution binding rules do not establish account permissions. |
| `U-SIZING-WRITE` | Exact observation, contention, wait and pin write grants and administrative exceptions. `pipelineOfBoundRun` is a mandatory boundary, not a complete write-role matrix. |
| `U-RUNNER` | Roles and grants for listing and managing runners. Team-admin reservations are specified, but every listing/management permission is not. |
| `U-GITHUB-ADMIN` | Installation, automation, trigger, extra-repo and Actions-binding management permissions, distinct from App webhook trust and issued push credentials. |
| `U-SCHEDULE` | Roles and grants for schedule listing, repository registration, overrides, pause, resume, disarm and manual invocation. The rename is specified, the per-operation rights are not. |
| `U-QUEUE` | Grants for slot actions, queue reads and administrative force-release. Bound slot operations have no admin bypass. Any distinct override authority needs an independent contract. |
| `U-OPERATOR-READ` | Console team/waitlist reads and waitlist-approval permissions. Console write-power boundaries do not specify every query role. |
| `U-DEPLOYMENT` | Exact operational grants and role distinctions for limits, signup policy, object-store breaker controls, quota/allowance and storage settings. The allowed categories are fixed, not their full route matrix. |
| `U-BILLING` | Customer versus service permissions for checkout closure, cards, payment submission/status, warnings and credit settings/history. Automatic settlement requires the signed Stripe proof above. Refund/grant/freeze console boundaries remain mandatory. Stripe test keys do not remove authorization requirements. |
| `U-TOKEN` | Independent member/machine/service credential CRUD, list, rotate and metering-administration rights. Delegate/Award/Exchange semantics do not grant root-management permission. |
| `U-GIT-ADMIN` | Roles for storing/deleting git credentials, confirming them, inspecting release audits and recording owner machine opt-in. Execution release constraints remain fixed. |
| `U-SECRET-ADMIN` | Roles for secret list/get/set/delete/rotate and variable views, distinct from declared execution release. |
| `U-INPUT-PATH` | Whether a final direct output path names the caller job or the authorized source job. Dependency/reference reads must remain possible. Do not apply `ownNode` to a source identifier merely because the old route contains a node parameter. |
| `U-BUILD-TRUST` | `trust_local_builds` survives under frozen §4.2/§8.1. The text does not specify the member or console role that may read or change this team setting. |
| `U-CLAIM-CONTROL` | Precise fixed-unit grants and methods for renewal and launcher synchronization. No root-token or stale-claim bypass can be inferred from an old route. |
| `U-REPORT` | Which fixed report grant covers each activity, annotation, metric, usage, touch, step and summary operation, and which owner-side reports are allowed. Bindings alone are insufficient. |
| `U-APPROVAL` | Approver roles, read permissions and owner-request grants for approvals. The transition actor label does not confer deployment-admin console authority. |
| `U-CONTROL` | Retained debug, dispatch, finish and child-queue controls and their grants. The architecture deletes some mechanisms but does not enumerate every wire control. |
| `U-OBSERVABILITY` | Audience, tenant scope and grants for metrics, trends and sizing-discovery queries. |
| `U-MAINTENANCE` | Whether any HTTP orphan-reconciliation route survives and what authorizes it. Leased internal maintenance does not imply a public administrative endpoint. |

## Source routes

The exact source spelling is retained in the first column, including `/metrics`, parameter names and wildcard braces. Rows are grouped only for reading. A repeated rule reference does not merge away any old route.

### Account deletion moves to the console

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/accounts/{account}` | unresolved | Account deletion is console-only. C2 must confirm whether this path remains under console authority or is replaced. | `credentials-matrix-authentication-status` | OPS; U-ACCOUNT-DELETE |
| `DELETE /api/v1/me` | unresolved | Self-service deletion authority is removed. C2 must confirm the final console deletion path. | `credentials-matrix-authentication-status` | OPS; U-ACCOUNT-DELETE |

### Schedule resource renames

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/crons/repos` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `DELETE /api/v1/crons/{id}/override` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `GET /api/v1/crons` | rename | `GET /api/v1/schedules` | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `GET /api/v1/crons/{id}` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `POST /api/v1/crons/{id}/disarm` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `POST /api/v1/crons/{id}/pause` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `POST /api/v1/crons/{id}/resume` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `POST /api/v1/crons/{id}/run` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `PUT /api/v1/crons/repos` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |
| `PUT /api/v1/crons/{id}/override` | rename | Schedule resource `/api/v1/schedules`; C2 names item/override/repository operations. | `credentials-matrix-authentication-status` | U-SCHEDULE |

### GitHub App administration

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/github-app/installations/{installation_id}` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `DELETE /api/v1/team/github-app/automation` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `DELETE /api/v1/team/github-app/installations/{installation_id}` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `DELETE /api/v1/team/github-app/triggers` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `GET /api/v1/team/github-app` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `GET /api/v1/team/github-app/automation` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `GET /api/v1/team/github-app/extra-repos` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `GET /api/v1/team/github-app/installations/{installation_id}/repositories` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `GET /api/v1/team/github-app/triggers` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `PUT /api/v1/team/github-app/automation` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `PUT /api/v1/team/github-app/extra-repos` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |
| `PUT /api/v1/team/github-app/triggers` | unresolved | App integration survives. Frozen text does not choose these management paths or roles. | `credentials-matrix-pipeline-and-audience` | U-GITHUB-ADMIN; PUSH does not grant management rights |

### Account, team and invitation administration

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/me/identities/{provider}` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `DELETE /api/v1/team` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `DELETE /api/v1/team/invitations/{id}` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `DELETE /api/v1/team/members/{user_id}` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `DELETE /api/v1/teams/{team}` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/auth/whoami` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/me` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/me/identities` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/me/team-deletions` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/team/build-trust` | unresolved | The `trust_local_builds` setting survives. C2 must confirm its resource path and administration rights. | `credentials-matrix-authentication-status` | U-BUILD-TRUST |
| `GET /api/v1/team/invitations` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `GET /api/v1/team/members` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `PATCH /api/v1/team` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `PATCH /api/v1/team/members/{user_id}` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `POST /api/v1/invitations/{id}/accept` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `POST /api/v1/me/active-team` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `POST /api/v1/team/invitations` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `POST /api/v1/teams` | unresolved | C2 must confirm the existing resource operation and independent role/binding rights. | `credentials-matrix-authentication-status` | U-ACCOUNT |
| `PUT /api/v1/team/build-trust` | unresolved | The `trust_local_builds` setting survives. C2 must confirm its resource path and administration rights. | `credentials-matrix-authentication-status` | U-BUILD-TRUST |

### Run resource and customer controls

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/runs/{id}` | keep | Run resource `/api/v1/runs` and detail `/api/v1/runs/{run}` | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs` | keep | Run resource `/api/v1/runs` and detail `/api/v1/runs/{run}` | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}` | keep | Run resource `/api/v1/runs` and detail `/api/v1/runs/{run}` | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/attempts` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the attempts operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/events` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the events operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/events/stream` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the stream operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/receipt` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the receipt operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/steps` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the steps operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `POST /api/v1/runs/{id}/cancel` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the cancel operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `POST /api/v1/runs/{id}/events` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the events operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |
| `POST /api/v1/runs/{id}/finish` | unresolved | FinishRunAndUnit survives. C2 names its final path. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL for wire operation mapping |
| `POST /api/v1/runs/{id}/retry` | unresolved | Run resource `/api/v1/runs/{run}`; C2 names the retry operation. | `credentials-matrix-trigger-and-parent` | RUN; U-RUN |

### Secret administration and release

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/secrets/{name}` | keep | Secret resource `/api/v1/secrets`; C2 must separate execution release from account administration and name rotate. | `credentials-matrix-pipeline-and-audience` | U-SECRET-ADMIN |
| `GET /api/v1/secrets` | keep | Secret resource `/api/v1/secrets`; C2 must separate execution release from account administration and name rotate. | `credentials-matrix-pipeline-and-audience` | U-SECRET-ADMIN |
| `GET /api/v1/secrets/{name}` | keep | Secret resource `/api/v1/secrets`; C2 must separate execution release from account administration and name rotate. | `credentials-matrix-pipeline-and-audience` | SECRET; U-SECRET-ADMIN |
| `POST /api/v1/secrets` | keep | Secret resource `/api/v1/secrets`; C2 must separate execution release from account administration and name rotate. | `credentials-matrix-pipeline-and-audience` | U-SECRET-ADMIN |
| `POST /api/v1/secrets/rotate` | unresolved | Secret resource `/api/v1/secrets`; C2 must separate execution release from account administration and name rotate. | `credentials-matrix-pipeline-and-audience` | U-SECRET-ADMIN |

### Team git credential administration

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/team/git-credentials/{host}` | unresolved | Team credential storage, opt-in and release audits survive. C2 names their administration paths. | `credentials-matrix-pipeline-and-audience` | SOURCE; U-GIT-ADMIN |
| `GET /api/v1/team/git-credentials` | unresolved | Team credential storage, opt-in and release audits survive. C2 names their administration paths. | `credentials-matrix-pipeline-and-audience` | SOURCE; U-GIT-ADMIN |
| `GET /api/v1/team/git-credentials/releases` | unresolved | Team credential storage, opt-in and release audits survive. C2 names their administration paths. | `credentials-matrix-pipeline-and-audience` | SOURCE; U-GIT-ADMIN |
| `POST /api/v1/team/git-credentials` | unresolved | Team credential storage, opt-in and release audits survive. C2 names their administration paths. | `credentials-matrix-pipeline-and-audience` | SOURCE; U-GIT-ADMIN |
| `POST /api/v1/team/git-credentials/{host}/confirm` | unresolved | Team credential storage, opt-in and release audits survive. C2 names their administration paths. | `credentials-matrix-pipeline-and-audience` | SOURCE; U-GIT-ADMIN |

### Runner resource renames

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/team/github-runners/{repository_id}` | rename | GitHub runner resource `/api/v1/runners/github`; C2 names item paths. | `credentials-matrix-pipeline-and-audience` | PUSH for issued credentials; U-GITHUB-ADMIN for binding management |
| `GET /api/v1/agents` | rename | `GET /api/v1/runners` | `credentials-matrix-pipeline-and-audience` | U-RUNNER; AUTH |
| `GET /api/v1/team/github-runners` | rename | GitHub runner resource `/api/v1/runners/github`; C2 names item paths. | `credentials-matrix-pipeline-and-audience` | PUSH for issued credentials; U-GITHUB-ADMIN for binding management |
| `POST /api/v1/team/github-runners` | rename | GitHub runner resource `/api/v1/runners/github`; C2 names item paths. | `credentials-matrix-pipeline-and-audience` | PUSH for issued credentials; U-GITHUB-ADMIN for binding management |

### Token and capability administration

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `DELETE /api/v1/team/tokens/{prefix}` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `DELETE /api/v1/tokens/{prefix}` | keep | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `GET /api/v1/team/tokens` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `GET /api/v1/tokens` | keep | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `GET /api/v1/tokens/{prefix}` | keep | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `POST /api/v1/team/tokens` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `POST /api/v1/tokens` | keep | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `POST /api/v1/tokens/{prefix}/metered` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN; BILLING-PROVENANCE |
| `POST /api/v1/tokens/{prefix}/rotate` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN |
| `PUT /api/v1/team/tokens/{prefix}/git-credentials` | unresolved | Token resource `/api/v1/tokens`; C2 distinguishes member/machine issuance from deployment service credentials. | `credentials-matrix-pipeline-and-audience` | ISSUE; U-TOKEN; SOURCE |

### Deleted state and administration surfaces

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/admin/usage-metrics` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/bounce` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/dispatches` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/auto-retry/reset` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/bounce` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/bounce/consume` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/status` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |
| `PUT /api/v1/teams/{team}/repos/{owner}/{name}/dispatch` | delete | No compatibility route. Use final state transitions or omit the deleted feature. | `surface-api-and-detail-parity` | CUT-SURFACE |

### Approval operations

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/approvals/pending` | unresolved | Run approval functionality survives. C2 names final paths and approver/requester rights. | `credentials-matrix-trigger-and-parent` | HOST for owner requests; U-APPROVAL |
| `GET /api/v1/runs/{id}/approvals` | unresolved | Run approval functionality survives. C2 names final paths and approver/requester rights. | `credentials-matrix-trigger-and-parent` | HOST for owner requests; U-APPROVAL |
| `GET /api/v1/runs/{id}/approvals/{nodeID}` | unresolved | Run approval functionality survives. C2 names final paths and approver/requester rights. | `credentials-matrix-trigger-and-parent` | HOST for owner requests; U-APPROVAL |
| `POST /api/v1/runs/{id}/approvals/{nodeID}` | unresolved | Run approval functionality survives. C2 names final paths and approver/requester rights. | `credentials-matrix-trigger-and-parent` | HOST for owner requests; U-APPROVAL |
| `POST /api/v1/runs/{id}/approvals/{nodeID}/request` | unresolved | Run approval functionality survives. C2 names final paths and approver/requester rights. | `credentials-matrix-trigger-and-parent` | HOST for owner requests; U-APPROVAL |

### Observability and sizing discovery

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/capacity/profiles` | unresolved | C2 must confirm operation, tenant scope, credential audience and final path. | `credentials-matrix-authentication-status` | U-OBSERVABILITY |
| `GET /api/v1/capacity/profiles/explain` | unresolved | C2 must confirm operation, tenant scope, credential audience and final path. | `credentials-matrix-authentication-status` | U-OBSERVABILITY |
| `GET /api/v1/trends` | unresolved | C2 must confirm operation, tenant scope, credential audience and final path. | `credentials-matrix-authentication-status` | U-OBSERVABILITY |
| `GET /metrics` | unresolved | C2 must confirm operation, tenant scope, credential audience and final path. | `credentials-matrix-authentication-status` | U-OBSERVABILITY |

### Deployment limits and object-store operations

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/compute-limits` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `GET /api/v1/object-store/breaker` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `GET /api/v1/signups` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `GET /api/v1/storage` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `POST /api/v1/object-store/reset-breaker` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `PUT /api/v1/compute-limits` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `PUT /api/v1/signups` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `PUT /api/v1/storage/quotas/{principal}` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `PUT /api/v1/storage/quotas/{principal}/allowance` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |
| `PUT /api/v1/storage/settings` | unresolved | Deployment operations live under `/api/v1/operator/*`; exact paths and read/write grants are C2 assumptions. | `credentials-matrix-pipeline-and-audience` | OPS; U-DEPLOYMENT |

### Concurrency slots and queue operations

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/concurrency/{key}/holder` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `GET /api/v1/concurrency/{key}/resolve` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `GET /api/v1/concurrency/{key}/state` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `POST /api/v1/concurrency/{key}/acquire` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `POST /api/v1/concurrency/{key}/cancel-waiter` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `POST /api/v1/concurrency/{key}/force-release` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT prohibits a bound-route admin bypass; U-QUEUE must specify any distinct force-release authority |
| `POST /api/v1/concurrency/{key}/heartbeat` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |
| `POST /api/v1/concurrency/{key}/release` | unresolved | Queue resource `/api/v1/queue/groups/{group}` is specified. C2 must name runtime slot and administrative operations. | `credentials-matrix-node-child-slot` | SLOT; U-QUEUE |

### Billing and credit APIs

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/credits` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `GET /api/v1/credits/history` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `GET /api/v1/credits/payments/{reference}` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `GET /api/v1/credits/settings` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `GET /api/v1/credits/units` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `GET /api/v1/team/billing` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/credits/card-payments` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/credits/card-refunds` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | OPS; U-BILLING |
| `POST /api/v1/credits/cards` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/credits/checkouts/closed` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/credits/freezes` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | OPS; U-BILLING |
| `POST /api/v1/credits/grants` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | OPS; U-BILLING |
| `POST /api/v1/credits/reversals` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | OPS; U-BILLING |
| `POST /api/v1/credits/warnings` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/team/billing/card` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/team/billing/checkout` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `POST /api/v1/team/billing/pay` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `PUT /api/v1/credits/settings` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |
| `PUT /api/v1/team/billing/budget` | unresolved | Billing ships on test keys. Frozen text does not fix these route paths or the service/member permissions. | `credentials-matrix-authentication-status` | U-BILLING |

### Deleted cache service and grants

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/gitcache/git/{path...}` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `GET /api/v1/runs/{id}/gitcache/git/{path...}` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/cache/store-ceiling/measure` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/cache/store-ceiling/thaw` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/gitcache/git/register` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/gitcache/git/{path...}` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/runs/{id}/cache-grant` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/runs/{id}/gitcache/git/register` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |
| `POST /api/v1/runs/{id}/gitcache/git/{path...}` | delete | No cache-service or cache-grant route. Surviving object transfers use controller `/data`; C2 names signing routes. | `surface-api-and-detail-parity` | CUT-CACHE; DATA for replacement transfers |

### Operator console operations

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/operator/teams` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS; U-OPERATOR-READ |
| `GET /api/v1/operator/teams/{team}` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS; U-OPERATOR-READ |
| `GET /api/v1/operator/waitlist` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS; U-OPERATOR-READ |
| `POST /api/v1/operator/teams/{team}/freeze` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS |
| `POST /api/v1/operator/teams/{team}/grants` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS |
| `POST /api/v1/operator/teams/{team}/trust` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS |
| `POST /api/v1/operator/teams/{team}/unfreeze` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS |
| `POST /api/v1/operator/waitlist/approve` | unresolved | Console-only powers stay separate from deployment operations. C2 must state the final console path and session rights. | `credentials-matrix-pipeline-and-audience` | OPS; U-OPERATOR-READ |

### Pipeline listing and latest run

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/pipelines` | unresolved | Pipeline and run resources survive. C2 names listing/latest routes. | `credentials-matrix-pipeline-and-audience` | PIPE for bound-claim access; U-RUN for account reads |
| `GET /api/v1/pipelines/{name}/latest` | unresolved | Pipeline and run resources survive. C2 names listing/latest routes. | `credentials-matrix-pipeline-and-audience` | PIPE for bound-claim access; U-RUN for account reads |

### Pipeline sizing

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/pipelines/{name}/profile` | rename | `GET /api/v1/pipelines/{name}/sizing` | `credentials-matrix-pipeline-and-audience` | PIPE |
| `POST /api/v1/pipelines/{name}/profile/contention` | rename | Sizing resource `/api/v1/pipelines/{name}/sizing`; C2 names the contention operation. | `credentials-matrix-pipeline-and-audience` | PIPE; U-SIZING-WRITE |
| `POST /api/v1/pipelines/{name}/profile/observations` | rename | Sizing resource `/api/v1/pipelines/{name}/sizing`; C2 names the observations operation. | `credentials-matrix-pipeline-and-audience` | PIPE; U-SIZING-WRITE |
| `POST /api/v1/pipelines/{name}/profile/waits` | rename | Sizing resource `/api/v1/pipelines/{name}/sizing`; C2 names the waits operation. | `credentials-matrix-pipeline-and-audience` | PIPE; U-SIZING-WRITE |
| `PUT /api/v1/pipelines/{name}/profile/pin` | rename | Sizing resource `/api/v1/pipelines/{name}/sizing`; C2 names the pin operation. | `credentials-matrix-pipeline-and-audience` | PIPE; U-SIZING-WRITE |

### Queue resource rename

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/queue/state` | rename | `GET /api/v1/queue` | `credentials-matrix-authentication-status` | U-QUEUE |

### Job and run logs

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/grep` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/logs` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/logs/search` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/logs/{node}` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/logs/{node}/completeness` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/logs/{node}/stream` | unresolved | Run/job log reads stay controller-proxied. C2 names final tail/search paths. | `credentials-matrix-node-child-slot` | LOG; U-RUN for account reads |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/logs` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names log reads, writes and signing operations. | `credentials-matrix-node-child-slot` | LOG; OWN; HOST; U-RUN for account reads |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/logs/stream` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names log reads, writes and signing operations. | `credentials-matrix-node-child-slot` | LOG; OWN; HOST; U-RUN for account reads |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/log-manifest` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names log reads, writes and signing operations. | `credentials-matrix-node-child-slot` | LOG; OWN; HOST; U-RUN for account reads |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/log-segments` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names log reads, writes and signing operations. | `credentials-matrix-node-child-slot` | LOG; OWN; HOST; U-RUN for account reads |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/logs` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names log reads, writes and signing operations. | `credentials-matrix-node-child-slot` | LOG; OWN; HOST; U-RUN for account reads |

### Child runs and child output

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/{id}/children/{childID}` | unresolved | Child operations survive under the run resource. C2 names the child routes. | `credentials-matrix-node-child-slot` | CHILD |
| `GET /api/v1/runs/{id}/children/{childID}/nodes/{nodeID}/output` | unresolved | Child operations survive under the run resource. C2 names the child routes. | `credentials-matrix-node-child-slot` | CHILD; DATA |
| `POST /api/v1/runs/{id}/children` | unresolved | Child operations survive under the run resource. C2 names the child routes. | `credentials-matrix-node-child-slot` | CHILD; BODY |

### Debug and dispatch control

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/{id}/debug-pauses` | unresolved | Run debug/child-queue functionality must be assigned a final resource by C2. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 must determine retained debug or dispatch operation and rights. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 must determine retained debug or dispatch operation and rights. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `GET /api/v1/runs/{id}/paused` | unresolved | Run debug/child-queue functionality must be assigned a final resource by C2. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `GET /api/v1/runs/{id}/pending-triggers` | unresolved | Run debug/child-queue functionality must be assigned a final resource by C2. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `POST /api/v1/runs/{id}/debug-pauses` | unresolved | Run debug/child-queue functionality must be assigned a final resource by C2. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 must determine retained debug or dispatch operation and rights. | `credentials-matrix-trigger-and-parent` | HOST; U-CONTROL |

### Job resource reads

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/{id}/nodes` | rename | `GET /api/v1/runs/{run}/jobs` | `credentials-matrix-node-child-slot` | RUN; U-RUN |
| `GET /api/v1/runs/{id}/nodes/{nodeID}` | rename | `GET /api/v1/runs/{run}/jobs/{job}` | `credentials-matrix-node-child-slot` | OWN; U-RUN |

### Job reports and telemetry

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/{id}/nodes/{nodeID}/metrics` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/activity` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/annotations` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/metrics` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`. C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`. C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/summary` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/touch` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/usage` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names final job/step reporting operations. | `credentials-matrix-node-child-slot` | OWN; HOST; U-REPORT |

### Declared inputs and outputs

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/runs/{id}/nodes/{nodeID}/output` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}` and controller `/data`; C2 names final output/record operations. | `credentials-matrix-node-child-slot` | DATA; U-INPUT-PATH |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}` and controller `/data`; C2 names final output/record operations. | `credentials-matrix-node-child-slot` | OWN; DATA; HOST for owner-side writes |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/claim/input` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names input resolution. | `credentials-matrix-node-child-slot` | OWN; DATA |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/output-commit` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}` and controller `/data`; C2 names final output/record operations. | `credentials-matrix-node-child-slot` | OWN; DATA; HOST for owner-side writes |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/output-upload` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}` and controller `/data`; C2 names final output/record operations. | `credentials-matrix-node-child-slot` | OWN; DATA; HOST for owner-side writes |

### Service discovery with unresolved rights

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/services` | unresolved | C2 must confirm whether the route survives and who can use it. | `credentials-matrix-pipeline-and-audience` | U-SERVICES; AUTH is insufficient to choose positive cells |

### Deleted customer trigger routes

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `GET /api/v1/triggers` | delete | `GET /api/v1/runs` | `surface-api-and-detail-parity` | CUT-TRIGGER; U-RUN |
| `GET /api/v1/triggers/spawned-child` | unresolved | Customer trigger resources are removed. Internal owner-claim and child-control path spellings require C2 confirmation. | `credentials-matrix-trigger-and-parent` | AWARD; HOST; CHILD where applicable; U-CLAIM-CONTROL |
| `GET /api/v1/triggers/{id}` | delete | Run detail `/api/v1/runs/{run}` | `surface-api-and-detail-parity` | CUT-TRIGGER; RUN; U-RUN |
| `POST /api/v1/triggers` | delete | `POST /api/v1/runs` | `surface-api-and-detail-parity` | CUT-TRIGGER; BODY; U-RUN for account-role intake rights |
| `POST /api/v1/triggers/claim` | unresolved | Customer trigger resources are removed. Internal owner-claim and child-control path spellings require C2 confirmation. | `credentials-matrix-trigger-and-parent` | AWARD; HOST; CHILD where applicable; U-CLAIM-CONTROL |
| `POST /api/v1/triggers/{id}/claim` | unresolved | Customer trigger resources are removed. Internal owner-claim and child-control path spellings require C2 confirmation. | `credentials-matrix-trigger-and-parent` | AWARD; HOST; CHILD where applicable; U-CLAIM-CONTROL |
| `POST /api/v1/triggers/{id}/done` | unresolved | Customer trigger resources are removed. Internal owner-claim and child-control path spellings require C2 confirmation. | `credentials-matrix-trigger-and-parent` | AWARD; HOST; CHILD where applicable; U-CLAIM-CONTROL |
| `POST /api/v1/triggers/{id}/heartbeat` | unresolved | Customer trigger resources are removed. Internal owner-claim and child-control path spellings require C2 confirmation. | `credentials-matrix-trigger-and-parent` | AWARD; HOST; CHILD where applicable; U-CLAIM-CONTROL |

### Claim and launcher control

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/launcher/claim` | unresolved | Claim functionality survives. The frozen text does not name these final route paths. | `credentials-matrix-pipeline-and-audience` | AWARD |
| `POST /api/v1/launcher/sync` | unresolved | Claim functionality survives. The frozen text does not name these final route paths. | `credentials-matrix-pipeline-and-audience` | AWARD; U-CLAIM-CONTROL |
| `POST /api/v1/nodes/claim` | unresolved | Claim functionality survives. The frozen text does not name these final route paths. | `credentials-matrix-pipeline-and-audience` | AWARD |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/claim` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names the named-claim operation. | `credentials-matrix-pipeline-and-audience` | AWARD |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat` | unresolved | Job resource `/api/v1/runs/{run}/jobs/{job}`; C2 names the claim renewal operation. | `credentials-matrix-node-child-slot` | OWN; U-CLAIM-CONTROL |

### Maintenance control

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/maintenance/reconcile-orphans` | unresolved | Reaper work survives as a leased maintenance pass. No HTTP maintenance right is specified. | `credentials-matrix-pipeline-and-audience` | U-MAINTENANCE |

### Run intake

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/runs` | keep | `POST /api/v1/runs` | `credentials-matrix-trigger-and-parent` | BODY; U-RUN for account-role intake rights |

### Deleted run heartbeat

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/runs/{id}/heartbeat` | unresolved | Only the owner lease may determine liveness. C2 must confirm whether this path renews that lease or disappears. | `credentials-matrix-trigger-and-parent` | HOST; U-CLAIM-CONTROL |

### Unified job transitions and expansion

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/runs/{id}/nodes` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/attempt` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/deps` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/execution-finish` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/execution-start` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/finalize-ready` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/finish` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/release` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/revoke-ready` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |
| `POST /api/v1/runs/{id}/nodes/{nodeID}/start` | unresolved | One TransitionNode report operation and AcceptPlan/AcceptExpansion creation. C2 names the wire paths. | `credentials-matrix-trigger-and-parent` | NODE-WRITE; HOST; EXPANSION where jobs are inserted |

### Bound credential release

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/runs/{id}/oidc-token` | unresolved | OIDC mint operation survives. C2 names its final path. | `credentials-matrix-node-child-slot` | OIDC |
| `POST /api/v1/runs/{id}/source-credential` | unresolved | Source release survives. C2 names its final path. | `credentials-matrix-node-child-slot` | SOURCE |

### Plan acceptance

| Old route | Disposition | Final resource/path or C2 assumption | Destination scenario ID | Independent rule / unresolved right |
|---|---|---|---|---|
| `POST /api/v1/runs/{id}/plan` | unresolved | AcceptPlan survives. C2 names the final acceptance path. | `credentials-matrix-trigger-and-parent` | HOST; PLAN |

## Completion record

The inventory has 229 unique source routes: 12 keep, 22 rename, 20 delete, 175 unresolved. Each route has one primary destination scenario and at least one independent rule or unresolved-right reference. This historical inventory records source routes independently of the final declaration baseline above.

The complete source dimensions remain in [the port inventory](t1-port-inventory.md). The appendix supplies the missing route dimension. It does not close the unresolved expected answers. Launch acceptance remains incomplete until the lead resolves the named rights and T1 encodes those answers independently of the production route table.

<!-- design:end -->
