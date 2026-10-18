# W3 native source continuation

The intake gate stays strict. The native fixtures now supply resolved execution
and code commits. Audited App source credentials now carry their approved extra
repositories through the controller and host client to acquisition.

## Implementation and inventory

The [source inventory](W3-SOURCE-INVENTORY.md) lists every acquisition,
materialization and directory-based SDK/engine selection path found in the prior
session. A new search confirms clone/checkout/worktree execution remains under
`internal/source`, including its dependency-free `gitcmd` leaf. Native assembly
still leaves unaccepted SDK classification to Acquire. Shared local/remote policy
materialization, moved-HEAD coverage and the ownership guard remain in place.
W9's action_plan.go preview adapter still has a caller and was left to W9.

The latest native report failed because shared fixtures omitted commits, used
`exact-commit`, or initialized repositories without HEAD. The local/here/initial
trigger fixture owners now carry both IDs. Compute-limit and flood fixtures carry
matching IDs, including when flood tests vary the commit. The repository SDK
integration resolves its real HEAD. Real preparation fixtures establish an initial
commit. The build-policy proof checks both selected IDs against that HEAD while
retaining WorkingTree and its ignored payload. Unsafe-location rejection tests
also use valid IDs, so the new gate cannot hide their intended validation.

Invariant: a source release exposes only the extra repositories approved in the
same audited transaction as its App token. C1 returns the existing
`source.Credential` type with that list, after rechecking both names and IDs across
token minting. C2 serializes optional `extra_repositories`, and its client passes
that list to acquisition. No list is inferred from caller input or token-provider
metadata. Static SSH/HTTPS credentials gain no App extras.

The client seed reproduced the dropped-list failure before the fix. The store
property varies approved names and IDs and exercises changes during minting,
revocation, claim expiry and failed audit. Refusals return no secret or list.
The native HTTP proof retains its ID-scoped token/rename assertions and checks
that the approved list survives the route.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, and GOTMPDIR/TMPDIR=/tmp/sw-astra-W3. Tests used -p 4.

Passed:

- Whole-repository `go build ./...` and focused vet across source, runspec,
  settings, nodehost, localcontroller, store, controller, client and CLI.
- Full source, runspec, nodehost and localcontroller suites:
  12.644s, 1.765s, 29.165s and 4.017s.
- CLI local/here/owner/initial-trigger/fuzz-seed selection: 39.956s.
  The final build-policy and initial-trigger rerun passed in 13.363s.
- Controller compute-limit, flood, team-waiter and claimed-SDK regressions:
  2.747s. Intake policy/unsafe-location checks: 0.216s.
- Store GitHub source and scheduled-source properties: 7.106s.
  Client source-access seeds: 0.016s. Focused race checks of client source access
  and store release/audit properties: 1.051s and 63.251s.
- Existing Lifecycle, Environment and Credentials acceptance selections: 0.156s.
  This includes lifecycle request/plan seals, environment origin-HEAD/guards and
  credential source-release/host cells. Placement matrix cells still skip.
- Commentcheck against main: clean across 2,871 files. Diff whitespace is clean.

Could not run: `TestRunSourceCredential_MintsTheApprovedRepositoriesByID` and
`TestSourceCredential_AppScheduleUsesLiveAutomationConsent`. Their fixture HTTP
servers panic on the sandbox's socket refusal before the relevant assertions.
The lead must run them, the offline CLI proof and request-ID socket integration,
and the whole repository suite. No suite failure was converted to a skip.

No acceptance cell was newly enabled and no archived test was newly ported.
The current W3-native-latest.md is headed 11:56 MDT and still contains FAIL.
It predates this working diff. Native completion is unconfirmed.

## Contract changes made

- C1 `ReleaseGitHubSourceCredential` returns `source.Credential` rather than
  `githubapp.Token`. The controller caller and all store test callers migrated.
- C2 SourceCredentialResponse and its client carry optional extra_repositories.
- C1, C2 and the W3 source-selection continuation record the audited-list contract.
  No route, schema, grant, setting, timeout or environment variable was added.

## Open questions and integration

1. DESIGN: strict executable identity excludes unborn local checkouts. Unsealed
   inspection still works. The explicit requested gate now requires an initial
   commit for executable admission. If local execution before a first commit must
   remain supported, the lead must settle a resolved snapshot identity contract.
   No placeholder or empty commit ID was admitted to bypass the invariant.
2. The TypeScript SDK's job clone helper remains W5/lead's cross-language ownership
   question. The Go ownership guard passes. The complete inventory names it.
3. W9 still owns preview/describe, child local-secret inheritance and public docs.
   No file in those concerns was edited. No acquisition stub remains in W3.
4. The earlier SQLite follower-cancellation DESIGN remains open. The full nodehost
   suite passes here. This session did not change its cancellation mechanism.
5. Native App, Windows, Darwin, PostgreSQL and deployed-runner evidence is left to
   the lead. The latest native report prevents a COMPLETE result.

## Shared files and volume

Production: `pkg/store/github_source_release.go`,
`pkg/controller/source_controller.go`, `pkg/controller/client/source_access.go`.
Their test callers and the native fixture migrations are the shared test edits:
`cmd/sparkwing/{here_resume,local_profile,run_build_policy,run_host,run_offline_unix,
run_request_id,trigger_intake}_test.go`, `internal/settings/resolution_test.go`,
`pkg/controller/{claim_source,compute_limits,engine_claim_sdk_unix,flood,
intake_policy}_test.go`, `pkg/controller/client/source_access_test.go` and
`pkg/store/github_source_release_test.go`.

This session replaces the bare-token release return with its source credential
and deletes 21 production lines while adding 29, net +8. W3 production against
main is +12,435/-26,049, net -13,614 by the lead's exclusions, within its -2,300
budget. The previous session's legacy source import and direct-checkout cuts
remain deleted. No compatibility wrapper was added.

Friction: native sockets are unavailable; Overwing's installed binary reports
it is ten commits behind source, but standards matching and reading work.
Lead-owned handoffs/amendments and W9-owned files are unchanged. No Git writes,
operational CLI execution, release or deployment ran. Checks joined before
scratch removal. /tmp/sw-astra-W3 was removed; /tmp/sw-gocache was retained.

STATUS: CONTINUE
