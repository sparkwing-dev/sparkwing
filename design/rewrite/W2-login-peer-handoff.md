# W2 browser login and transport identity

<!-- design:begin -->

Starts from abc054faa. No Git writes. Changes remain in the assigned worktree.

## Implemented and deleted

GET/POST /login use PolicyRouter and the existing browser proof. The form
authenticates a live unbound account capability through the shared verifier,
then calls C1 CreateCapabilitySession. This removes the retired token-table
login, admin-scope requirement, argon2/cache dependency and limiter middleware
registration. Existing client/listener login budgets stay in the proof.
One account token and matching same-origin form proof are required. Duplicate
or unknown fields and ambiguous presentations fail before issuance. Errors
use the canonical envelope. Login-page reads do not renew sessions.

The old 271-line browser_login_test.go is deleted. Its ten regressions are
ported in t1-port-inventory.md. The sole remaining setCookie test-helper caller
in browser_pages_test.go now inspects the response cookies directly.

Default console authentication reads the real committed account-session
snapshot and the current configured operator accounts. Only direct account
sessions acquire console identity, and only on routes offering it. They receive
OperatorConsole alone. Ordinary routes keep account identity and member grants.
CSRF checks the selected session's hash, account, team and persisted-key-derived
token. Console BindNone applies deployment-wide without selecting the account's
active team as a boundary. Other bindings still use the store checker.
Dashboard page gating uses the live session reader without renewal and applies
the same direct-session requirement to operator pages.

WithPeerIdentity replaces WithPeerPrincipal. The callback supplies only the
transport-verified local subject. It cannot supply grants or tenancy. Explicit
headers and session cookies take precedence without falling back to the peer.
The api.sock caller uses its kernel-verified uid. The shared verifier sends
that subject to AuthenticationReader through CredentialLookup.Peer.

The production Go diff removes 126 lines and adds 222, including the minimal
shared caller edits. Go tests remove 282 lines and add 474. These counts include
new files and exclude prose/generated snapshots. No legacy compatibility API,
new environment variable, SQL implementation or second issuer was added.

## Invariants and evidence

A browser session remains within its live account capability's grants and
ancestry. Console authority comes only from a configured operator account's
direct sign-in. Local authority comes only from the transport-verified subject.

Six active TestLoginHTTP tests exercise real SQLite, cookies, narrowed grants,
parent revocation, replacement, invalid credentials/forms, safe redirects,
canonical errors, method refusal, script escaping and audit attribution.
Three TestConsoleSessionHTTP tests cover direct-session proof, member identity,
operator configuration changes, cross-team deployment reads, CSRF and refusal
of bearer/capability-parented sessions. The metering transaction is a fake.
TestPolicyPeerIdentityProperty uses fake C1 snapshots and bindings through the
real router and default identity selector. Generated properties forge form,
console and transport proofs. No native credential matrix cell is completed.

## Contract changes and real integrations left open

Local C2 interpretations now describe account-capability browser login and
direct-session console identity. No C1 or foreign contract signature changed.

- C1 ReadAuthentication refuses CredentialPeer. CheckBinding resolves every
  principal through a persisted capability/session AuthorityRef. W1 must supply
  the existing peer snapshot/checker interfaces and the transactional local
  actor representation. The fake proves W2 transport selection, not real
  api.sock execution. Native Windows still requires verified SID transport.
- Console mutation methods still need authority-bearing committing rechecks.
  The default proof adapter does not make legacy grant, trust, freeze or
  settings writes transactional. Service-root provisioning also still expects
  a persisted operator/service capability instead of a verified console account.
- W1's cancellation-reason authentication projection remains absent in
  authentication_binding.go. W2 continues using BindingState.CancelReason.
  The first job intent must precede run intent when W1 supplies the snapshot.
- Startup bootstrap still consumes a supplied raw token through the retired
  token table. ProvisionCapability generates a new presentation. The existing
  W1/W2/W6/W8 delivery-contract request remains unresolved.
- Identity linking still depends on absent identity_link_states storage and
  needs committing authority rechecks. Roleless sessions remain a C1 gap.
  No nonce table, in-memory substitute or alternate link issuer was added.
- Other inherited C1 transaction, audit, logical-record, enrollment, metering,
  rotation and financial-event integrations remain as listed in handoffs.md
  and the prior W2 handoffs. Legacy bearer/claim middleware and unmigrated
  account/team and execution routes remain. W2 is not complete.

A1-20/21/22/23 advance through the login and identity adapters. W2's completed
parts of A1-01/03/05/08/14/15/24/25/26/27/28/29/30/31/32/33/34/35/38/39/40/49/55
retain their prior implementations and integration requests. A1-53 remains
native integration work for T1/lead. No finished mechanism was rebuilt.

## Shared files

- internal/orchestrator/wingd_api.go migrates the production peer caller.
- internal/orchestrator/hosted_retry_test.go migrates its sole peer fixture.
- internal/apiroutes/apiroutes.go discovers the new named login endpoints.
- .apidiff/pkg_controller.txt follows the public peer API and operator GoDoc.
- docs/auth.md, docs/security.md, CHANGELOG.md and their generated mirrors
  describe the changed browser and console behavior.
- perf/budgets.json keeps the login corpus exemption and names unmeasured
  page/action latency, cold start and response size.
- C2, handoffs.md and t1-port-inventory.md record the contract and test ports.

The API/OpenAPI generator ran. Its generated reference and schema had no diff.
No W1 source, schema, TypeScript SDK or native transport implementation changed.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Commands use go -C /home/bombuntu/.xwing/envs/1080/sparkwing. Tests use -p 4.

- Whole-repository go build passes.
- Full executable tests/acceptance passes. Existing native skips remain.
- Full capability, apierr, apiroutes, apiref and apispec tests pass.
- Controller policy router, peer property, login budgets, handler-policy,
  route guards and tenant-route inventories pass.
- Scoped vet passes for controller, acceptance, apiroutes and orchestrator.
- Native Linux TypeScript command passes in 12.6 seconds:
  go test -p 4 ./sdk/typescript/test/nativehost -run '^TestNative' -count=1.
  No setup failure occurred. This does not prove the credential matrix.
- API snapshot, changed docs/mirrors, changelog, scrub and commentcheck against
  main pass. The previous unrelated retired-SDK doc-symbol failure is not
  rerun or repaired here.

The old login test was red against the retired token verifier. Console tests
were red against the former ordinary-session projection. Deleting the old login
test helper initially broke browser_pages_test.go compilation. Its minimal
caller migration closes that failure, and the focused checks and vet pass.

No provider network, PostgreSQL, native browser/Unix socket/Windows/Darwin,
performance, deployment, pipeline or independent-review result is claimed.
The lead owns pipelines and review under the brief. Installed sparkwing info
ran once during bootstrap before the brief's executable prohibition was read.
No further Sparkwing executable ran. Scratch is deleted after verification.

STATUS: CONTINUE

<!-- design:end -->
