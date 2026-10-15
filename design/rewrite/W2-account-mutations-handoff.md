# W2 account mutations and integration merge

<!-- design:begin -->

## Merge

Started at 1b306fd7a with bw-3095-rewrite already merging. All 19 conflict
contents are resolved. The index still reports them as unmerged because the
swarm brief forbids Git writes. The lead must stage the resolutions and finish
the merge. No conflict marker remains. Amendments-1.md and handoffs.md match
MERGE_HEAD byte for byte.

W2's policy routes survive. Integration's artifact-manifest client/storage API
removal survives. Its retired wingd HTTP implementation and route tests stay
deleted: 401 and 334 lines respectively in the worker's starting HEAD. The
hosted retry test keeps both capability and wingd-client imports. Integration's
obsolete profile tests stay retired. Duplicate credit registrations are not
restored. The final credit fixture uses the existing teamTenant helper.

Migration and port-inventory records from both sides survive without duplicate
sections. Security docs retain the storage-counter hard cut. Auth docs retain
integration's owner-fenced transition description. The OpenAPI annotation route
keeps its detailed description and loses the duplicate seed. API snapshots,
references and embedded mirrors are regenerated.

## Account mutation cut

These four routes now use PolicyRouter and its existing account-session policy:

- POST /api/v1/me/active-team
- POST /api/v1/teams
- POST /api/v1/invitations/{invitation}/accept
- DELETE /api/v1/me/identities/{provider}

They accept the selected cookie or Session-header presentation, require its CSRF
and same-origin proof, and do not renew it. The actor and selected session come
from Authorized. Query/header account, team and session selectors grant nothing.
Team creation retains normalization, slug validation and the existing three-team
limit. Selection permits existing names, including default. Closed invitations
use canonical conflict because the shared error registry has no gone code.
Unknown providers return not_found. Persistence diagnostics stay private.

The five old handler/helper implementations occupied 123 lines. They are
replaced by four Authorized handlers. The nine-line switchTeam helper is fully
deleted. Four old registrations and four tenant-catalog entries are deleted.
The account router also loses its duplicate registration implementation in
favor of the existing shared registrar. Two obsolete tests lose 27 function
lines, plus their separating whitespace. These counts exclude integration's
independent changes.

C2 now declares AccountWriter and WithAccountWriter. Its four methods are:

```text
SelectAccountTeam(ctx, Authorized, Team) error
CreateAccountTeam(ctx, Authorized, TeamInfo) (TeamInfo, error)
AcceptAccountInvitation(ctx, Authorized, invitationID) (InvitationAcceptance, error)
UnlinkAccountIdentity(ctx, Authorized, provider) (sessionsEnded, error)
```

Each transaction must reread the selected hashed session and complete ancestry
under the store clock, derive the account, and refuse ended or changed
authority. Team selection, creation and invitation acceptance must commit
account and selected-session team changes together. Retain membership, invitation
email/expiry/use rules, creation limits and admission accounting. Unlinking must
recheck the existing ten-minute sign-in window, protect the last sign-in method,
end every other account session and retain GitHub App installations.

The default requires Store to implement that interface and otherwise returns
policy_missing. No legacy Store method is used as a fallback. C1 must provide
the production transactions. The implementation and atomicity of those writes
are not claimed complete.

Mechanism added: one W2-owned transaction interface and setter. The smaller
alternative, calling existing raw account methods, cannot recheck authority or
commit account/session selection together. Reverting this cutover removes the
interface and restores the prior routes. No C1 signature, schema, clock, key,
environment variable or compatibility route changed.

## Evidence and ports

Invariant: account mutations affect only the authenticated account and selected
session, and persistence rechecks that authority at commit.

TestAccountMutationHTTPIdentityProperty generates forged account/team/session
selectors against real SQLite authentication and fake transactions. It checks
the exact committed session authority passed to the writer, normalized create
input, the path invitation/provider, unchanged session expiry and private output.
TestAccountMutationHTTPProofProperty generates invalid CSRF, origin, bearer and
ambiguous-cookie presentations. None reaches a writer or renews the session.

TestAccountMutationHTTPAuthorityRefusals covers member bearers, machine roots,
claims, console and peer authority, ended sessions and backend refusal.
TestAccountMutationHTTPTransactionRefusals covers committed-authority refusal,
nonmembership, missing rows, used invitations, email mismatch, last sign-in
method, recent-sign-in refusal and private backend errors. Input/missing-writer
tests cover empty or invalid bodies, unknown fields/providers, unavailable
transactions and unsupported methods.

The deleted TestActiveTeamSwitchNeedsMembership and
TestIdentityRoutesRefuseABearerToken move to those properties and refusal tests.
Actual sticky selection, later sign-in and atomic team/session changes remain
C1 integration obligations. Existing store identity tests retain the persistence
requirements. Other legacy identity/link/team tests still need their final
ports. These are component proofs. No native credential-matrix cell is newly
unskipped or claimed complete.

## Verification

All Go commands use the assigned worktree with go -C, GOWORK=off,
GOFLAGS=-buildvcs=false, GOMAXPROCS=4, GOCACHE=/tmp/sw-astra-W2/cache and
GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes after the final implementation.
- Vet passes for controller, controller/client, capability, apierr, acceptance
  and orchestrator.
- The broad W2 executable acceptance selection, including engine adapters,
  passes in 44.213 seconds. Its selection is
  `^Test(Account|Authority|Credential|Data|Secret|Source|Session|Console|Login|OAuth|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine)`.
- The final account/session/credential/data/source selection passes again in
  12.524 seconds after the AccountWriter signature was simplified. New account
  tests also pass alone. No native matrix cell is inferred from these runs.
- Controller policy, authorized-handler, route and tenant guards pass with
  `Test(Policy|AuthorizedHandlers|RouteGuard|TenantRoute|SessionRoute)`.
- Full capability, apierr, apiroutes, apiref and apispec tests pass.
- Native Linux TypeScript passes in 22.367 seconds with
  `go test -p 4 ./sdk/typescript/test/nativehost -run '^TestNative' -count=1 -timeout=180s`.
  There is no TypeScript setup failure.
- commentcheck against main is clean across 1642 files. OpenAPI, API-snapshot,
  changelog and whitespace checks pass.

Two controller real-store tests remain red:
TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences and
TestEngineOwnerReconcilesExecutesAndFinishes both report
`holder transition requires a nonnegative source attempt`. W9 still must carry
the captured SourceAttempt. W2 does not infer an ordinal from a later read.

A broader controller selection reached TestOperatorPageServesOnlyTheOperatorsSession
and panicked because httptest cannot bind in this sandbox:
`listen tcp6 [::1]:0: socket: operation not permitted`. Full pkg/docs similarly
panicked in TestWebClient_Versions_HappyPath. Neither is a passing suite.
The socket-free docs selection found 17 stale SDK symbol references in existing
authoring/cache/CLI/SDK/local-execution docs, including Await, CacheKey, Memoize
and WhenRunner. Mirror, changelog and embedded scrub checks reported no failure
in that selection. Those W9 documentation cuts were not made here.

The first account-test compile found a nonexistent fake helper, corrected before
the passing run. OpenAPI validation caught an unquoted comma in the changed
invitation description, corrected before regeneration and the passing check.
Neither failure was hidden by a test skip or relaxed check.

## Amendment and handoff audit

| Assignments | Disposition |
| --- | --- |
| A1-01/03/05 | Existing node-service policy and canonical error decoder retained. W3 owns real host composition. |
| A1-08/09/19/26/38/55 | Existing claims, enrollment lookup, private awards, exchange and here adapters retained. Production origins/profiles and immutable launcher-instance correlation remain external. |
| A1-11/14/15/16/17 | Existing source, failure, cancellation, snapshot and execution adapters retained. SourceAttempt transport still needs W9. |
| A1-20/49 | Shared root issuer retained. Mounted raw bootstrap delivery and production origin/enrollment persistence remain open. |
| A1-21/22/23 | Four more actual routes cut over. Default console, CSRF and transport-verified peer adapters remain. C1 still lacks peer authority and cancellation-reason projection. |
| A1-24/25 | Transactional bindings, source audit and plan-secret seams retained. No W2 SQL copy or alternate issuer. |
| A1-27 | Shared billing calculator retained; no second calculator. |
| A1-28 | Signed committed-checkout settlement retained. Verified card/refund/warning/dispute contracts remain joint C1/C2 work. |
| A1-29/30/31/32/33/34/35 | Bound data/record/cache adapters retained. Production metadata/read-set/charging/backend composition remains integration work. |
| A1-39/40/53 | Runner/local-controller/native composition remains with its owners and T1/lead. No fake evidence is promoted to native evidence. |
| W1 cancellation snapshot | BindingState.CancelReason already exists and W2 consumes it. authenticationBindingTx still selects only cancellation timestamps; W1 must populate the declared reason with job intent before run intent. |
| W9/W2 typed step reports | Existing RecordStep adapter retained. Captured attempt transport remains W9 work. |

## Remaining work and shared files

New C1 request: implement AccountWriter as declared above. Successful production
account mutation is refused until it exists. Other C1 requests from prior W2
reports remain: roleless sessions, verified peer authority, console mutation
rechecks, final-schema account deletion, root origin/rotation/enrollment,
credential mutations/maintenance, sizing, source audit, data and provider-event
integration. No foreign contract signature was changed here.

Priority 1 is incomplete. auth.go, claim_route.go and the remaining legacy
server registrations still serve execution/reporting, source/OIDC, concurrency,
team/GitHub App and billing routes. Browser identity linking still uses its old
session path and needs the declared durable nonce/authority commit integration.
Only the four account mutation handlers and their old registration paths were
cut in this batch. Do not delete the remaining shared middleware before migrating
its users. No new product decision or customer limit was introduced.

Shared paths touched to resolve the merge or regenerate its surface:

- internal/orchestrator/hosted_retry_test.go and the two deleted wingd_api files
- pkg/controller/client/client.go and pkg/storage/state.go
- api/openapi.yaml and .apidiff snapshots
- docs/auth.md, security.md, migrations/_unreleased.md and api-reference.md
- CHANGELOG.md and their generated pkg/docs mirrors
- design/rewrite/contracts/t1-port-inventory.md for the port record

All other implementation changes are in W2's controller, C2 contract and
acceptance files. There is no caller migration outside those listed merge
paths. The lead owns staging, the merge commit, independent review and pipelines.
Temporary cache files are removed at the end of the session.

STATUS: CONTINUE

<!-- design:end -->
