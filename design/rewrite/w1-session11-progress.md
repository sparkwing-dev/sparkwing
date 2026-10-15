# W1 continuation

Starts at aa64caa9e. W1 remains incomplete. W1b's accounting, blob and log
implementations are unchanged. The lead owns review, commits and landing.

## Completed work

Implemented the store portion of the new cancellation/session handoff.
Cancellation intent has its own cancel_reason column on runs and nodes. The
first cancellation owns that reason. Repeated requests and holder completion
cannot replace it. Request-only edges no longer write terminal error, reason,
exit code or failure data. Run cancellation carries the reason to affected jobs.
Job, owner and eval renewal return it through LeaseBeat. Reopening preserves it.

Session creation, lookup, renewal and expiry now use the final schema and
nanosecond timestamps. Sessions store SHA-256 digests. The authority reader derives
grants from current membership and narrows capability-backed sessions to their
parent's grants. Parent team, account, audience and unbound identity must match.
Team switching requires a live account session and membership in its destination.
Capability-backed sessions retain their original team. The controller preserves
the store's verified scopes in cookie principals and session responses.

The old role map no longer grants triggers.read because the final C2 registry
excludes it. This does not add a new role or permission.

SessionKeyCipher supplies the existing bound cipher to the store. The CSRF key
is a sealed settings row. The first minter wins atomically and invalidates sessions
without an available signing key. Replicas use the same committed envelope.
Missing cipher configuration, wrong keys and malformed envelopes fail loudly.
Server.WithSecretsCipher installs the existing controller cipher on the store.

Removed CreateTokenSession, its **33-line implementation**, and Session.TokenPrefix.
CreateCapabilitySession derives identity from a live unbound account capability
and retains it as a revocation parent. Removed caller clocks from account/identity
session creation and both lookup APIs. No alias or forwarding compatibility API
remains. Replaced **178 lines** of legacy token-table lookup and raw metadata-key
persistence. Deleted the obsolete **29-line** token-session test and ported its
parent-expiry/revocation assertions to TestCapabilitySessionEndsWithItsCapability.
Ported TestSessionRenewalKeepsLaterExpiryAndRefusesDeletedRow to the final schema
and an injected store clock.

## Invariants and evidence

- Cancellation intent stays independent of holder results. The generated property
  covers 32 reasons/orderings, repeated requests, job/owner renewal and draining.
- No reusable session or CSRF signing secret is persisted in plaintext. The
  32-case persistence property checks digest rows, sealed envelopes, replica
  CSRF agreement, forged presentations and deletion.
- Session authority cannot exceed current membership or its capability parent.
  Properties cover 16 membership sequences, 16 generated parent identities and
  16 generated grant subsets through the registered session HTTP handler.
  The handler uses ServeHTTP without a network listener. Its origin verifier is
  a contract fake.
- Expired sessions never renew. The property generates 32 lifetimes and checks
  exact-expiry and post-expiry boundaries through renewal and digest lookup.

Added the passing acceptance cell
TestCredentials/credentials-session-digest-csrf/store-sqlite. It creates a session,
checks digest authentication and sealed key storage, reopens the SQLite store,
checks unchanged CSRF/timestamps, demotes the member and revokes the session.
Strengthened recovery-cancel-beats-loss/store-sqlite to retain the first reason
through draining, reaping and reopening. Native cells retain their required skips.

## Contracts and remaining work

C1 records the two cancellation columns, row fields, SessionKeyCipher and the
session API cuts. The store API snapshot changes only these declarations. C1,
the handoff and T1 record the new evidence.

**Foreign contract change needed:** C2 BindingState has RunCancelled but no
cancellation reason. W2 must add the reason field, or declare its final equivalent,
before W1 can project intent through AuthenticationReader. W1 has left that
foreign Go contract unchanged. LeaseBeat and store snapshots already carry it.

Session integration still needs W2's real OAuth/browser middleware and origin
verification. The final schema cannot represent a teamless session, while legacy
onboarding requests one. C2 must resolve that representation. No nullable team,
implicit team or new grant policy was invented here.

The previous amendment audit still applies. A1-27 cannot award metered work:
authority_issue.go refuses unavailable reservations, and C2 SettleBilling still
panics. W1b must supply the unstarted refund and customer-wait interval contract
before claim/start/reap/finish wiring can complete. Materialization and result
record joins still use materializationRecords and nodeResultRecords fakes.
Public record methods with independent transactions cannot implement those joins.
SyncLauncher still panics and requires W8/W9's immutable Kubernetes correlation.
LocalRuntime wiring and native transports/processes remain other-area integration.

StateStoreOld, legacy Store.AcceptPlan/validation, old claims/triggers, offer rounds
and unleased maintenance still have callers. Their retirement remains W1 work.
The scope clarification authorizes minimal migration of their remaining callers,
including affected persistence fixtures. This continuation retires the session
surface only. W1 is not complete.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test/build parallelism is -p 4.

- Whole-repository go build ./... passes.
- go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The store selection passes **313 top-level tests / 913 cases**, with no skips.
  Extract Test names from w1-session11-tests.txt, omit TestPostgres-prefixed names
  and join them into an anchored -run alternation. The role-scope isolation test
  also passes independently.
- Controller TestEngine.* and TestSessionRoutePreservesCapabilityScopeProperty
  pass. Shared controller test packages compile after clock/capability migration.
- Full tests/acceptance and pkg/match pass. Required native skeleton skips remain.
- Required commentcheck -base main passes across 949 Go files. Session
  sleepcheck -base HEAD and git diff --check pass. Go files are formatted.
- The ten changed/new public store declarations match generated API output.
  Other preexisting snapshot drift remains the aggregate regeneration owner's work.
- Git has no local user.name, user.email or core.worktree override.

An overbroad controller selection reached old session_hash/session_lifetime
fixtures that bootstrap the removed tokens table. Those failures occur before
the changed paths. The explicit final-schema controller selection passes. The
full legacy store/controller suites were not claimed. PostgreSQL, race, lint,
native browser/process/provider proofs, pipelines and independent review remain
lead checks.

## Shared files and friction

Shared production caller changes are pkg/controller/auth_handlers.go,
browser_login.go, identity.go and server.go. Minimal clock/capability fixture
migrations are pkg/controller/browser_session_test.go,
github_onboarding_browser_test.go, identity_signin_test.go, revocation_test.go and
team_boundary_test.go. The new controller component property is
pkg/controller/session_store_runtime_test.go.

Shared acceptance files are internal/acceptance/store_credentials.go,
store_recovery.go and the new store_sessions.go. Shared documentation is
.apidiff/pkg_store.txt, contracts/handoffs.md, contracts/t1-acceptance.md and
contracts/t1-port-inventory.md. Store fixture migrations also touch deletion_test,
identity_link_test and identity_test. Other changes stay inside W1 store files.

Friction came from legacy seconds/token/meta SQL, a stale role grant, controller
scope replacement and the removed-token browser fixtures. The first controller
probe used the wrong route and was corrected to /api/v1/auth/session. Mechanical
clock migration briefly touched the controller's unrelated private lookup helper
and left unused test imports/variables. Those changes were corrected before the
passing build and vet.

The new mechanism is SessionKeyCipher because the final schema requires an
encrypted key and the controller already owns the cipher. Keeping a plaintext
hex key cannot satisfy that contract. Reverting session_key.go and its setter
callers reverses the wiring. No new key file, environment variable, service,
compatibility surface or customer limit was added.

No Sparkwing executable, Git write, service, deployment or release ran. All owned
check processes completed. Scratch is deleted before reporting.

STATUS: CONTINUE
