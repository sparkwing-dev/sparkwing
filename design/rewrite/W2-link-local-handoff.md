# W2 browser linking and local HTTP continuation

<!-- design:begin -->

## Implemented and deleted

The two browser identity-link routes now use PolicyRouter's named browser
proof. The old formCSRF registrations, Principal-based authorization helpers,
redirect-only refusal handler and direct ConsumeIdentityLinkState/LinkIdentity
calls are removed. The old link implementation loses 273 lines across
browser_oauth.go and identity_link.go. Four replaced tests remove 106 lines.
The 253-line replacement includes proof verification and the committing adapter.

Linking requires one direct provider session, same-origin form CSRF at start,
recent sign-in, MAC-bound state, PKCE and one-use nonce consumption. The writer
receives the session digest, never its presentation. It consumes state before
provider access and rechecks authority when attaching the verified profile.
Provider errors pass a separate sanitizer, including canonical errors nested
inside transport errors. Success alone clears the flow and redirects. Failed
proofs return the shared error envelope without renewing the session.

Nonlocal dashboard pages and login no longer consult the legacy AuthEnabled
switch. Pages require their own session. Dashboard.Local remains the explicit
local-page mode. Unknown page mutations return the shared method refusal.

Server.HTTP() supplies W8's ratified wingd.LocalHTTP contract. It constructs the
final API registrations once, projects only Wingd's verified connection identity
and keeps explicit headers/cookies ahead of peer identity. It returns the shared
dashboard handler separately. The API omits the legacy router fallback and
returns canonical not_found for missing paths. The runtime retains lifecycle
and drain ownership.

## Invariants and evidence

A provider identity attaches only to the same recently signed-in account session
that started its one-use link. Generated HTTP properties vary session/account,
headers, cookies, form fields, provider, state, verifier and flow mode. Refusals
never enter the nonce writer or provider. The successful flow checks the exact
session digest, selected account, profile and no session renewal. Replay reaches
neither provider nor identity attachment. Further cases cover old/dead sessions,
missing writers, backend errors, provider verification failures and a session
ending during provider access. Durable store rechecks are fake boundaries.

Provider diagnostics never enter link responses or logs. A generated test wraps
arbitrary canonical errors, including private detail keys, as direct, wrapped and
joined transport failures. They all become the safe unavailable envelope.

Local authority comes only from verified socket connection identity. The real
Wingd HTTP server runs over net.Pipe with fake committed peer facts. Generated
header/cookie selectors cannot override that identity or fall back to it after
explicit-credential refusal. Direct unverified API and dashboard requests fail.
Three retained legacy paths return not_found. Server shutdown joins the requests.

Four old browser-link tests are ported. The former open-controller login
expectation is reversed to require explicit local mode for local pages. No
native T1 credential, browser, UID/SID, placement or local-runtime cell is marked
complete from these components.

## Contracts and real integrations left open

C2 now declares capability.IdentityLink and controller.IdentityLinkWriter,
supplied with WithIdentityLinkWriter. The full committing requirements are in
c2-credentials.md. W1 must implement nonce binding/consumption and profile
attachment against current direct-session authority, using its store clock.
Completion must reject a revoked session and atomically mark the consumed nonce
complete with the identity uniqueness checks. The old missing nonce table and
unfenced LinkIdentity are not default adapters. No C1 contract, schema or SQL
implementation changes here.

W1's authentication_binding.go still reads only cancellation timestamps. C2's
BindingState.CancelReason already exists and the controller already projects it.
W1 must populate the first committed job intent before run intent in that same
snapshot. W2 does not introduce a second cancellation read.

W1 still owns the committed peer authority snapshot and local mutation fences.
W8/W9 still need the full LocalController lifecycle/store/host factory to consume
Server.HTTP(). Console proof projection is unchanged. Roleless/direct-console
session representation and committing console writes remain production gaps.

Priority 1 is incomplete. auth.go, claim_route.go, retained legacy execution,
trigger and team routes, GitHub App browser flows and their remaining callers
still need retirement. The startup bootstrap cut still needs W1's approved
seeded ProvisionSpec/Store.Issue contract. That foreign signature is unchanged.
The approved provider-event extension beyond checkout remains joint W2/W1b work.

The prior concurrency handoff's credential, source, OIDC, data/read-set, rotation,
enrollment and billing transaction integrations remain open. This continuation
does not reclassify their fake evidence as production integration.

## Amendment and handoff disposition

- A1-21/22/23: browser linking now uses the shared proof boundary. Local HTTP
  projects verified transport identity. Remaining legacy route cuts stay open.
- A1-40 and lead decision 5: the W2 shared-handler adapter is implemented against
  W8's existing LocalHTTP interface. Lifecycle composition stays with W8/W9.
- W1 cancellation handoff: C2 consumption is implemented. W1 projection remains
  absent in this checkout and is listed above.
- A1-01/03/05/08/09/11/14/15/16/19/20/24/25/26/27/28/29/30/31/32/33/34/35/38/39/49/53/55
  retain the implementations and open owner integrations recorded in
  W2-concurrency-handoff.md. Seeded startup and verified non-checkout events
  remain unfinished. No lead-owned amendment or handoff ledger changes.

## Shared files and mechanisms

All production changes are within W2's ownership. internal/apiroutes discovers
the new actual browser registrations and tests their named proof. Acceptance
fixtures add a provider transport failure field and reverse the obsolete
tokenless nonlocal page expectation. Source docs, embedded mirrors, the public
controller API snapshot and performance manifest follow those changes.

New mechanisms are the two-method committing identity-link interface and the
cached LocalHTTP handlers. The interface replaces two unfenced store calls
because nonce consumption and final attachment both need live-session checks.
The cached handlers implement the lead's LocalHTTP decision and keep request
identity in the transport context. Both are removable within C2 without changing
C1 schema or another area's contract. No new credential class, key, environment
variable, rate limit or timeout is introduced.

## Verification and friction

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Tests use -p 4. Commands run with go -C or env -C on the assigned worktree.

Whole-repository go build ./... passes. Scoped go vet passes for capability,
apierr, apiroutes, controller/... and tests/acceptance. The final W2 acceptance
selection passes in 15.833 seconds. Its pattern is
^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient).
The controller policy/local HTTP selection passes in 0.140 seconds. All tests
in capability, apierr, apiroutes, apiref and apispec pass. Commentcheck is clean
across 2131 files. API spec, public API snapshot, changelog and whitespace
checks pass. Source docs and embedded mirrors match.

The provider-diagnostic property originally reused one account and reached the
existing attempt limit on its tenth generated completion. A fresh session/flow
fixture per case fixes that test setup. The corrected property passes in 1.664
seconds, followed by the final broad selection above. Production limits stay
unchanged. No Sparkwing
binary, pipeline, external service, Git write or TypeScript setup command runs.
Independent review and native acceptance remain with the lead. The scratch
directory is removed after checks finish.

<!-- design:end -->

STATUS: CONTINUE
