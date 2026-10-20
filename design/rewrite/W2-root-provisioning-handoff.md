# W2 root provisioning continuation

<!-- design:begin -->

This continuation starts from baad0b4cf. The checkout began clean. Earlier
credential, session, source, data, cancellation and payment work remains in
place. No Git write or Sparkwing executable ran.

## Implemented

Invariant: a root receives only its authenticated origin's permitted identity,
scope and grants, and failed origin verification exposes no new bearer.
TestRootProvisioningHTTPAuthorityProperty generates adversarial extra fields.
Nine new HTTP tests cover both roots, grants, CSRF, dead authority, exact origin
selection, transaction refusal and non-replayable routes.

POST /api/v1/runners registers through PolicyRouter with team.admin. The strict
body selects name, repository patterns, optional authorized pools, optional
narrow claim grants and optional absolute expiry. Omitted grants select
nodes.claim and triggers.claim. Explicit grants may narrow that set. The old
machine-name grammar and 32-pattern bound remain. The server derives the team,
machine subject and runner:<team>:<name> registration selector. The request
cannot select ancestry, metering, origin or sensitive release permissions.

POST /api/v1/operator/tokens registers with operator.tokens or console authority.
Its body selects name, operational grants and optional absolute expiry. The
server derives an empty team, service subject and service:<name> registration
selector. Grants are limited to claims.launch, operator.limits,
operator.object_store and operator.tokens. Console powers cannot be issued.

Both routes use the existing CredentialIssuer.ProvisionCapability and sole
Store.Issue path. No SQL, persistence interface, second registry, cache, clock,
environment variable or issuer was added. Responses expose capability once,
prefix, team, grants and optional RFC3339 expiry. Both routes use RetryNever,
no-store and canonical errors. Session mutations require CSRF. Audit targets
retain only the public token prefix. These request shapes and retry corrections
are local C2 contract changes.

The Go client has member, machine and deployment-service issuance requests.
Its six transport cases prove correct payloads and no replay after 503 with
Retry-After. The CLI callers consume the final response. Service creation no
longer accepts the redundant --type flag. The dashboard uses the authenticated
enrollment path and constructs its setup command from the returned capability
and grants. Owners see enrollment because the frozen policy requires team.admin;
editors retain their existing revocation controls.

## Deleted and ported

Deleted 128 lines of legacy implementation: team_tokens.go (97) and the
server-built credential setup/runner command helpers in identity_team.go (31).
The old route registration and tenant classification are removed too.
The client no longer exposes the kind/principal/scopes/TTL issuance request or
the Token/Metadata result. No forwarding route or retired payload alias remains.

Seven regressions were ported and their 213 old test lines deleted from
team_tokens_test.go, connect_command_test.go and runner_connect_internal_test.go.
The port inventory names each source and destination. Repository input refusals
now run at the final HTTP boundary. The dashboard command suite verifies actual
shell arguments, claim-loop selection, private replacement of a readable token
file and stop-on-setup-failure. The direct-source fixture retains its two
command-splitting helpers locally so the retained test still compiles.

## Real integration left open

SQLite HTTP tests commit and look up both root kinds through the real issuer
with a fake ProvisionPolicy. A refused policy leaves no credential behind.
These tests do not establish production enrollment or registration races.

W1 must implement the production origin policy within issuance: current
team-admin/deployment/console authority, registration identity and active-name
uniqueness, permitted pools/repositories, existing issuance limits, and retained
origin/registration facts for claims and rotation. Missing policy returns
unavailable. W1's existing ProvisionService origin check accepts only operator
or service subjects carrying operator.tokens. Verified console account sessions
need their final representation there. The console HTTP gate is tested with a
fake issuer. No foreign contract signature changed.

Bootstrap provisioning remains on the old startup path. W2 still owes login,
OAuth, console and verified-peer identity construction, remaining legacy route
cutovers and deletion of the old authorization stack. The existing card and
dispute callbacks need a verified-event extension from C1 before their legacy
service-bearer paths can be removed. Checkout callback verification and startup
configuration already exist and were not rebuilt here.

W9 still owns the wider CLI tree and service inventory/rotation selection,
runner failure-revocation flow and final engine/data client cutovers. W1/W8 own
the production claim-profile and runtime enrollment integration. The existing
W1 capability-manager, secret/source, maintenance and logical-record handoffs
remain open. Native topology, store/backend matrices and route-matrix cells
remain separate from these component proofs.

## Amendment disposition

A1-20 now has member, Actions, machine and service transport; bootstrap and
production root-origin policy remain open. A1-26 has its W2 enrollment transport;
production profile resolution remains W1/W8 work. A1-21/22/23 retain the earlier
ordinary session/snapshot/router work; identity construction and legacy stack
deletion remain W2 work. A1-28 retains the verified checkout callback and needs
the existing card/dispute contract extension. A1-01/03/05/08/09/11/14/15/16/19,
A1-24/25/27/29-35/38-40/49/53/55 retain the dispositions in
W2-capability-management-handoff.md and the cross-area handoffs. No previously
finished mechanism was rebuilt, and no native cell was marked passing from a
fake. The full W2 area remains incomplete.

## Shared caller and artifact files

Caller migrations outside W2 production code: internal/controller/client/admin.go;
cmd/sparkwing/cloud.go, runners.go, tokens.go and help_registry.go;
web/src/lib/teams.ts and web/src/app/team/machines/page.tsx.

Shared tests: the new tests/acceptance/root_provisioning_http_test.go and client
issuance test; tests/acceptance/capability_management_http_test.go;
internal/orchestrator/wingd_api_routes_test.go; cmd/sparkwing/runners_test.go;
web/src/lib/teams.test.ts. The capability-management test expects 404 after
removal of the last method at the old team-token collection. The runner scope
fixture moved out of production code and reflects root claim grants.

Artifacts: C2, handoffs.md and the port inventory; docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md; generated API/OpenAPI and CLI cluster
reference; embedded mirrors; the client API snapshot; two performance manifest
entries. Authorized performance remains unmeasured because its production
origin/registration corpus is missing.

## Verification

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache, and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Run from this worktree with absolute go -C paths. Tests use -p 4.

- Whole-repository go build passes.
- Full executable tests/acceptance passes, retaining its explicit native skips.
  Capability, apierr and apiroutes full suites pass.
- Nine new root-provisioning HTTP tests and the client issuance transport test
  pass. Existing inventory/rotation and focused controller, client, orchestrator
  and CLI route/flag/render checks pass.
- Scoped vet and commentcheck pass. API spec and embedded-doc sync checks pass.
  The changed client API snapshot matches the generator.
- All 44 dashboard team API tests pass, with no skips. TypeScript no-emit and
  targeted ESLint pass. Node child-process stdio must be inherited here because
  its pipe setup uses sandbox-blocked sockets; both actual shell proofs passed
  after that harness adjustment.

The full Go client suite could not complete: TestObserveSlotMapsMissingHolder
panicked when httptest.NewServer hit the sandbox's socket restriction.
Socket-free issuance and rotation tests pass. No product fallback or skipped
test was added. The lead retains native client HTTP, PostgreSQL, physical
backend, process/runtime, real-repository QA, independent review and pipelines.
The full snapshot and SDK-symbol documentation gates were not rerun here; the
prior handoff records their unrelated failures.

The ordinary CLI documentation generator runs a Sparkwing executable, which
the swarm brief forbids. A temporary Go-test overlay called the existing pure
splitCommandsMarkdown renderer instead and updated only cli-cluster.md. It did
not invoke main, a listener or product startup. Scratch overlays and caches are
deleted after the final checks. Git identity remains Korey's without local
identity or core.worktree overrides.

STATUS: CONTINUE

<!-- design:end -->
