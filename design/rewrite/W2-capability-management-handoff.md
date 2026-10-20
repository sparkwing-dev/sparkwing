# W2 capability management continuation

<!-- design:begin -->

This continuation starts from 2826096a0. The checkout began clean. Previous
W2 session, cancellation, source, data and credential work remains in place.
No merge, Git write or Sparkwing executable ran.

## Implemented

Invariant: credential metadata and replacements stay within authenticated
ownership and scope, and a failed transaction exposes no new bearer or partial
result. Generated HTTP properties cover foreign metadata, changed subjects,
replacement grants/audience/binding/metering, and secret-bearing audit targets.

Member inventory, detail and rotation use PolicyRouter at /api/v1/tokens and
its token resource. Deployment inventory, detail and rotation use the
corresponding /api/v1/operator/tokens paths. Existing issuance and revocation
registrations share that router. This removes the separate token-registration
wrapper. Unsupported methods return canonical errors. GET /api/v1/team/tokens
is removed. Its POST remains until machine enrollment is ported.

The collection uses member runs.read with BindNone because no existing token
selector exists on a collection. The committed reader filters owned
credentials or the current team admin's tenant. Resource reads and rotation
retain ownToken. Deployment routes retain operator.tokens or console authority.
Header and query team hints confer no authority. Session and console writes
require CSRF. All responses use no-store.

capability.Metadata is the explicit public projection. It carries subjects,
grants, owner, RFC3339 timestamps, source-consent state and the existing root
metering flag. Private digests, bindings, ancestry, pools and billing provenance
cannot enter this type. Scope-invalid results fail before any list is written.
Empty collections and grant sets are arrays. Filters reject duplicates,
malformed query encoding and unknown subject kinds. Store-clock selection
owns grace liveness. The HTTP projection preserves a committed future
revocation deadline without reading another clock.

Rotation accepts grace_secs and optional absolute expires_at. It preserves
the existing 24-hour default and seven-day ceiling, accepts explicit zero,
and rejects null, negative, overflowing and unknown inputs. Omitted expiry
preserves the original. Explicit expiry passes original-parent ceilings or
current root-origin checks. No additional cap on a root's old expiry is added.
The response returns capability once, new metadata, old_prefix and the
RFC3339 old_revoked_at. Both route policies use RetryNever because there is
no raw-secret replay contract. The Go client test verifies that a 503 with
Retry-After does not replay rotation.

## Persistence contract and integration points

C2 declares controller.CapabilityManager and Server.WithCapabilityManager.
Its shared types live in internal/capability so W1 can implement it without
importing controller. The interface is:

```go
Team() store.Team
ListCapabilities(context.Context, capability.Authorized, capability.InventoryFilter) ([]capability.Metadata, error)
ReadCapability(context.Context, capability.Authorized) (capability.Metadata, error)
RotateCapability(context.Context, capability.Authorized, time.Duration, *time.Time) (capability.Rotation, error)
```

The resolver selects the authenticated tenant, or empty team for Operator.
Missing and foreign-scope managers fail with policy_missing. There is no
legacy token-table fallback or controller SQL. The added manager is the
transaction seam the HTTP boundary needs. The smaller alternative of calling
legacy readers/RotateToken cannot recheck the authenticated capability or
preserve final capability origin and ancestry. Removing this resolver and its
fixtures reverses the injection seam.

W1 must implement these transactions with its store clock, live ancestry,
membership, ownership and route checks. Inventory selects controller
root/delegated account and machine credentials, or deployment service/operator
roots. Unit awards and node-host credentials are not inventory entries.
Rotation commits through Store.Issue and preserves origin, original parent,
registration, owner, repository/data scope, pools, metering and source consent.
The replacement cannot parent to the old credential that rotation ends.
The old deadline and replacement commit together and advance the old authority
generation. A repeated rotation of an already scheduled predecessor fails.
Explicit revocation ends grace at once. Raw never leaves a failed transaction.

Code inspection found a grace integration gap: readAuthorityTx in
internal/store/capabilities_contract.go rejects any non-nil RevokedAt. C2's
credentialAlive accepts a future deadline until that deadline. W1 must align
its persistence reader before claiming real grace or descendant integration.
No W1 code or foreign contract signature changed. No production inventory,
rotation, registration transfer or source-consent transfer is claimed here.

## Deleted and ported

Deleted 277 legacy lines across auth_handlers.go, tokens.go, team_tokens.go,
revocation_test.go and audit_test.go. This removes the old rotation handler,
request/response types, global token-list/lookup handlers, team-token list
handler/type and their obsolete rotation/audit fixtures. Removed their direct
registrations and tenant classifications. A stale GET secrets/{name} tenant
entry from the prior cutover was also removed after the inventory guard found
it. The remaining tokenRecordJSON belongs to the unported metering handler.

Three old tests are ported and deleted:

- TestRotateToken_GraceCapRejected and TestRotateToken_GraceAtCapAccepted are
  covered by TestCapabilityManagementHTTPRotation. It adds explicit zero,
  default grace, null/overflow refusal, both scopes and committed expiry.
- TestAuditKeepsABearerInATargetOutOfTheLog is covered by
  TestCapabilityManagementHTTPAuditProperty. Generated full bearers pasted
  into token selectors or invalid roles never enter logs. Token targets
  retain only their public 16-character prefix.

Nine new HTTP tests include three 100-case properties. The additional
committed-grace regression first failed with HTTP 500 and now passes. Two
socket-free Go client tests cover metadata decoding and non-replayed rotation.
No archived source-function identity or complete native acceptance cell is
claimed by these component proofs. Existing executable acceptance cells pass.

## Amendment and handoff disposition

The earlier completed work is documented in W2-session-policy-handoff.md and
its linked handoffs. It was not rebuilt.

| Rows | Remaining disposition |
| --- | --- |
| A1-01/03/05 | W2 contracts and shared decoder remain implemented. W3 owns the production process router and sink acknowledgements. |
| A1-08/09/14/15/19/26/38/55 | Existing claim, cancellation, failure and launcher boundaries remain. Production enrollment, identity construction, cancellation-reason authentication projection and native delivery remain with their recorded owners. |
| A1-11/16/17 | Sealed source and private execution projections remain. Consumer cutovers remain with W3/W8/W9. |
| A1-20 | Member and Actions issuance remain implemented. Inventory and rotation transports now use the C2 manager. W1's manager implementation, bootstrap, machine and service provisioning remain open. |
| A1-21/22/23 | Previous Session-header, lifetime and logout work remains. Login, OAuth, console/verified-peer identity, remaining legacy registrations and old auth deletion remain W2 work. |
| A1-24/25 | Existing source/secret release and plan-secret integration remain. W1 credential read/write/rotation/audit, source and trusted intake adapters remain open as recorded in handoffs.md. |
| A1-27/28 | Verified committed-checkout transport and billing helpers remain. Payment configuration and the other verified payment-event contracts remain open. |
| A1-29/30/31/32/33/34/35 | Signed-data and logical-record transports remain. Production authority/read/charge projections and real transfers remain open. |
| A1-39/40/49/53 | Runner/local-controller, private-file startup and native topology work remain with their recorded owners. |

The W1/W2 capability-manager handoff is added to contracts/handoffs.md.
Other existing handoffs remain open. The full W2 area is incomplete.

## Shared files

Outside W2's controller/capability code, production caller migrations are
internal/controller/client/admin.go, cmd/sparkwing/cloud.go, runners.go and
tokens.go, web/src/lib/teams.ts, web/src/app/team/machines/page.tsx, and
internal/apiroutes/apiroutes.go. They consume the new metadata/rotation shape
and inventory path. The CLI duration input converts to an absolute expiry.
No retired payload alias or forwarding route is added. The wider CLI
provisioning/enrollment cutover and the engine/data client stubs remain W9 work.

Shared test/inventory edits are tests/acceptance/authority_mutations_http_test.go,
the new capability_management_http_test.go, the client capability-management
test, cmd/sparkwing/tokens_test.go, web/src/lib/teams.test.ts,
internal/apiroutes/apiroutes_test.go,
internal/orchestrator/wingd_api_routes_test.go and
internal/acceptance/credential_routes.go. The local collection-policy correction
is recorded in contracts/t1-credential-routes.md and the three ports in
contracts/t1-port-inventory.md.

Documentation/artifact edits are C2 and handoffs.md, docs/auth.md, the migration
guide, CHANGELOG.md, generated API reference and OpenAPI, embedded mirrors,
the controller/client API snapshots and six performance manifest exemptions.
Authorized latency, cold start, response bytes and browser performance remain
unmeasured. Performance exemptions name the absent authenticated corpus and
transaction adapters.

## Verification

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache, and GOTMPDIR and TMPDIR under
/tmp/sw-astra-W2/tmp. Go tests use -p 4. All paths below refer to this worktree.

The full executable tests/acceptance suite passes, retaining its explicit
native skips. Capability, apierr and apiroutes suites pass. The final focused
credential/session/registration selection passes in acceptance, controller,
controller/client, orchestrator and CLI packages. The complete build and
scoped vet pass. The lead retains native transport, PostgreSQL, physical
backend, process-topology, real-repository QA, independent review and pipelines.

Final commentcheck passes across 1,177 Go files. API generation,
check-api-spec.sh, sync-docs.sh --check and git diff --check pass. Both touched
controller/client API snapshots match the generator. The full snapshot check
fails only because the unchanged .apidiff/pkg_runner.txt remains after that
package's removal. Its deletion belongs with the owning area's cutover.

The broader documentation symbol check fails on 17 existing removed SDK
symbols: Await, CacheDir, CacheKey, CacheKeyFn, DefaultCacheTTL,
DescribePipeline, JobApproval, JobFanOutDynamic, MaxCacheTTL, Memoize,
NoCache, NpmCache, Resolve, TTL, ToolCacheDir, ToolSlot and WhenRunner.
Those references are in unchanged SDK/authoring/cache/CLI/local-execution
docs and remain W4/W9 integration work. These failures are not green or
waived. The controller's changed schema and embedded mirrors pass their
checks. The default Git identity remains Korey's, with no local identity
or core.worktree override.

Reproduce the boundary evidence with:

```sh
go -C /home/bombuntu/.xwing/envs/1080/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./tests/acceptance ./internal/controller/client -run '^TestCapabilityManagement' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./internal/controller ./internal/orchestrator ./cmd/sparkwing -run '^(TestPolicyRouter|TestAuthorizedHandlersDoNotAuthorize|TestRouteGuard|TestTenant|TestEveryControllerRouteIsClassified|TestReadRouteListCarriesNoDuplicates|TestFormatScopes|TestRenderTokens)' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing run ./internal/commentcheck -base main .
```

The dashboard's 42 team API tests pass with direct Node/tsx execution, and its
TypeScript no-emit check passes. The ordinary Node --test invocation reports
only the file-level result in this environment. Direct execution verifies the
actual 42 assertions. No production listener or installed CLI was used.

Friction: missing persistence adapters, legacy controller fixtures and stale
Go-type references in OpenAPI. The removed response/list schema references
were replaced with the final shared types and generated route inventory.
Scratch and its caches are deleted after final checks. No new user limit,
clock, cache, issuer, environment variable or production service is added.

STATUS: CONTINUE

<!-- design:end -->
