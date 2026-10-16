# W2 operator team creation

<!-- design:begin -->

Decision 114 is implemented. W2's broader serving-registration check still has
the existing gaps listed below, so this report does not claim full-area acceptance.

## Implemented

- `sparkwing operator teams create --team SLUG [--display-name NAME]
  [--profile NAME]` calls one `POST /api/v1/operator/teams` operation.
- The real policy router accepts deployment `operator.tokens` or a configured
  direct operator-console session with CSRF. The store rechecks that authority.
- One transaction creates the team, a dedicated account without an OAuth
  identity, its owner membership, the first admin root through `Store.Issue`
  and a `team.created` audit naming the operator. Existing and retired slugs
  return conflict without writes. Issuance, audit and commit failures roll back.
- The response exposes the credential once. Its membership remains live, it has
  no deployment permissions or operator revocation parent, and ordinary member
  inventory, rotation and revocation apply. No free credit is issued.
- The common client never retries the operation, including lost responses and
  retryable HTTP failures. CLI plain/pretty emit the bearer once. JSON includes
  the team, generated account ID, capability, prefix and grants.
- Updated the auth guide, its CLI-example drift check, generated operator
  reference and embedded mirrors. Refreshed handwritten route expectations for
  the already recorded plan/eval and owner-process amendments. The serving
  inventory probes the local-only doctor registration on the local HTTP adapter.

The invariant is: only current operator authority can create a previously unused
team, and its first administrator and operator audit commit with that creation.
Generated HTTP cases vary grants, expiry, presentation and forged selectors.
Native tests also cover console CSRF, authority loss after route authorization,
two independent SQLite controller replicas, failed deferred commit and Cloud's
licensed deployment configuration. A separate generated store test refuses the
new origin outside the transaction that creates the team.

## Contract changes made

- C2: the new policy row, `ProvisionTeamAdmin` origin, exact owner grant table,
  transaction-local new-team gate, rotation behavior and one-time response.
- C1: concrete `Store.CreateOperatorTeam` and explicit audit actor on the existing
  private `createTeamTx`. Its account callers pass their own account ID.
- C3: `OperatorTeamCreate`, `OperatorTeamCreated` and
  `OperatorManager.CreateOperatorTeam`. No schema or migration changes.

The dedicated account is an implementation choice for operation without OAuth.
It carries no email verification or provider identity. A member token still
needs a real account and current membership, so fabricating a sign-in session or
weakening the member provisioning gate was rejected. The new trusted origin is
confined to the exact account and team created in its transaction.

## Evidence

Commands used `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`
and `GOTMPDIR=/tmp/sw-astra-W2/tmp`. Tests used at most `-p 4`.

- `go build -p 4 ./...` passed for the whole repository.
- `go vet -p 4` passed for capability, apierr, controllerwire, clisurface,
  controller, controller/client, store and cmd/sparkwing.
- Full capability, apierr, controllerwire and clisurface tests passed.
- All `TestOperatorCreateTeam*` HTTP tests passed, also under `-race` (7.222s).
  `TestOperatorTeamAdminOriginCannotMintIntoExistingTeam` passed.
- `TestOperatorTeamsCreateCLI*` passed through the real client, router,
  authenticator and SQLite store. Operator client regressions passed, including
  lost-response/no-replay and incomplete-response refusals.
- Focused store bootstrap, provisioning, identity, team mutation, rotation and
  business-event regressions passed (7.088s).
- Acceptance root provisioning, credential issuance, inventory, maintenance and
  default-store transaction tests passed (1.516s).
- `TestFinalCredentialRouteDeclarations` and
  `TestFinalCredentialRouteCoverageRejectsDrift` pass. No native topology matrix
  cell was unskipped or claimed from these tests. No historical test port was
  needed for the new operation.
- Auth examples, generated command reference, embedded-doc privacy and symbol
  checks pass. `sync-docs.sh --check`, commentcheck against main and
  `git diff --check` pass.

The full docs suite cannot run in this sandbox: its first web client test calls
`httptest.NewServer`, which fails to open a TCP socket with operation not
permitted. No whole-controller or whole-repository test pass is claimed.

## Remaining integration work and contract needs

`TestFinalCredentialServingRegistrations` still reports these existing gaps in
both listener configurations:

- `GET /api/v1/runs/{run}/jobs/{job}/output`
- `POST /api/v1/runs/{run}/jobs/{job}/claim/input`
- `POST /api/v1/operator/teams/{team}/refunds`

The doctor route exists on `Server.HTTP().API` and is absent from the hosted
listener by its C3 contract. Its inventory check now distinguishes transports.
The three missing routes have policy rows but no registered handlers. C1 has no
committing standalone job-output, claim-input or console-refund interface to
bind. `ReadChildOutput` checks a different relation, and raw payment reversal
helpers are not a console authority boundary. Their wire/transaction contracts
need to be settled before implementation. No permissive adapter, legacy
fallback or success stub was added. Decision 114 itself has no open native
integration point or question.

## Shared files and code volume

Outside W2, changed production files are:

- `pkg/store/operator_team.go`, `authority_provision.go`,
  `capability_rotation.go`, `dialect.go` and `identity.go`.
- `cmd/sparkwing/operator_team.go`, `main.go` and `help_registry.go`.
- `internal/clisurface/registry.go`, `internal/controllerwire/operator.go` and
  `pkg/controller/client/operator.go`.

Shared tests are `pkg/store/operator_team_test.go`,
`cmd/sparkwing/operator_team_test.go`, `cmd/sparkwing/docs_auth_test.go` and
`pkg/controller/client/operator_test.go`. C1/C3 contract notes and the auth/CLI
documentation mirrors changed. Lead-owned handoffs and amendments were untouched.

No legacy implementation was replaced by this additive operation. Removed or
replaced 20 production lines, including stale origin and retired cache-grant
commentary. Session production delta including shared files is +210/-20,
net +190. W2 alone is +37/-9, net +28. Against main, W2 is +16,566/-17,420,
net **-854**, within its -250 budget. Counts include untracked Go production
files and use the lead's acceptance/testdata/contract exclusions.

No git writes, installed binaries, services, releases or Sparkwing pipelines ran.
Generated CLI docs through their existing test renderer in a temporary Go
overlay because the swarm forbids running candidate Sparkwing binaries. The
shared cache is retained. Task scratch is deleted before handoff.

<!-- design:end -->

STATUS: CONTINUE
