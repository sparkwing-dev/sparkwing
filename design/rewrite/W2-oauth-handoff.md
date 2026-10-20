# W2 OAuth continuation

<!-- design:begin -->

This continuation starts from f0baa013e with a clean checkout. Earlier W2
implementations remain in place. No Git write or Sparkwing executable ran.

## Implemented

Invariant: only a callback matching the browser's provider, state, verifier and
configured redirect can create a session. Failed proof creates no session and
exposes no credential or upstream diagnostic through errors or audit records.

OAuth start and callback now register through PolicyRouter with AuthBrowserFlow.
Their verifiers own provider availability, redirect selection, rate admission,
flow-cookie uniqueness, state and code/error cardinality, PKCE exchange and
account-session creation. Handlers consume request-private proof and render the
browser result. HEAD and unsupported methods receive canonical 405 errors.
Refusals use the shared envelope, no-store and the existing Retry-After budgets.
Successful callbacks clear the flow, replace the old browser session and retain
the same-origin continuation page. Refused proofs preserve the flow cookie.

The link callback carries code/state to its same-site completion page. It
redeems no code and creates no session. Completed link cookies cannot enter
the callback again. Link start/completion still use their earlier implementation.
No substitute nonce registry or session issuer was added.

Provider failure logs retain the provider and rejection/failure category.
They drop raw provider errors because upstream text can carry reusable secrets.
The generated PKCE test varies upstream diagnostics as well as forged verifiers.
The login limiter now shares one admission function between its existing form
middleware and OAuth verifiers. Its limits and keying stay the same.

## Deleted and ported

Removed 104 lines from browser_oauth.go, including handler-side proof checks,
legacy HTML error rendering and the old link callback checker. The two direct
OAuth registrations are removed. The new proof boundary is 105 lines.

Deleted the 97-line browser_oauth_test.go. Its three regressions are ported to
tests/acceptance/oauth_http_test.go. Eight tests cover session authentication
and replay, 90 generated browser proofs, 64 generated PKCE failures, provider
and backend refusals, safe return paths, link continuation, rate limits,
unsupported methods and unknown providers. The port inventory names each
source and destination. These ports add no archived source-function identities.

## Contract changes needed in other areas

- C1 roleless sessions: CreateIdentityAccountSession requires a membership.
  A verified waitlisted account with no team cannot receive a session. The
  active refusal test records this gap. Creation, lookup, renewal and
  AuthenticationReader need one roleless authority representation that permits
  account operations and grants no team authority.
- C1 identity-link state: ConsumeIdentityLinkState queries identity_link_states,
  absent from the final schema. A temporary test overlay reproduced
  `SQL logic error: no such table: identity_link_states`. Supply durable one-use
  state before the link-completion cutover. The probe is removed with scratch.
- C1/C4 startup bootstrap: the credential file supplies a raw input secret,
  while ProvisionCapability/Store.Issue generate the raw result and accept no
  seeded presentation. EnsureBootstrapAdminToken still uses the retired token
  table. Resolve that contract mismatch while preserving transactional bootstrap
  gating and restart/revocation behavior. W2 changed no foreign signature.

No new interface, setting, environment variable, key, cache, lifetime limit or
compatibility path was introduced. The C2 update records this OAuth transport.

## Remaining W2 and integration work

A1-21/23 advance through real account-session creation and registered browser
proof. A1-20 still needs bootstrap. Login, console and verified-peer identity,
link start/completion, remaining legacy registrations and old authorization
deletion remain W2 work. A1-26 still needs production enrollment integration.
A1-28 retains the verified checkout callback and the missing C1 card/dispute
event extension. A1-53 native cells remain pending.

The other W2-assigned amendments keep the dispositions in
W2-root-provisioning-handoff.md and W2-capability-management-handoff.md. The
existing capability-manager, credential/secret, sizing, logical-record,
source-audit and maintenance transactions remain producer integration points
listed in contracts/handoffs.md. No finished mechanism was rebuilt here.

The GitHub client and SQLite session storage are real in these tests. The
provider transport is fake. Native browsers, live providers, Google verification,
PostgreSQL, full route matrices, deployment/runtime integration and performance
are unmeasured. No native cell is marked passing from this component evidence.

## Shared files and verification

Shared code: internal/apiroutes/apiroutes.go and apiroutes_test.go follow the
actual OAuth endpoint declarations. No other package calls the removed private
handlers. No production caller outside W2 needed migration.

The stale policy_store_test.go session fixture lacked the multi-team license
required by the existing session policy. It now shares the licensed fixture
from session_store_runtime_test.go. The real membership and CSRF checks pass.

Artifacts: C2, handoffs, T1 evidence and port inventory, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md and their embedded mirrors.
The HTTP paths already existed. The public API signatures and generated
server.go API inventory stay the same.

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
All Go commands use the absolute worktree with go -C. Tests use -p 4.

- Whole-repository go build passes.
- Full tests/acceptance, capability, apierr and apiroutes suites pass. Existing
  native skips stay explicit. The acceptance suite took 94 seconds.
- Scoped controller policy, session-scope and login-budget tests pass. The
  separate policy/signed-object and request-audit selection also passed except
  for the stale license fixture above, which was corrected and rerun.
- Scoped vet, commentcheck against main, API generation tests, docs mirror
  tests, sync-docs --check and git diff --check pass.

Reproduce the focused controller selection with:

```sh
go test -p 4 ./internal/controller -run '^TestPolicyRouter|^TestLoginLimiter_|^TestServerPolicy|^TestSessionRoutePreservesCapabilityScopeProperty$' -count=1
go test -p 4 ./tests/acceptance -run '^TestOAuthHTTP' -count=1
go run ./internal/commentcheck -base main /home/bombuntu/.xwing/envs/1080/sparkwing
```

The lead retains pipelines and independent review under the swarm brief.
Scratch is deleted after verification. Git identity remains Korey's with no
repository-local identity or core.worktree override.

STATUS: CONTINUE

<!-- design:end -->
