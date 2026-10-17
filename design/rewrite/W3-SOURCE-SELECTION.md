# W3 source selection and acquisition

Executable intake now requires resolved execution/code commits, and native
hosting classifies acquired code. The [native-source continuation](W3-NATIVE-SOURCE-CONTINUATION.md)
repairs the native fixtures and closes the live submodule metadata gap.
Cross-language ownership and the next native harness run remain open.
The full initial inventory is in [W3-SOURCE-INVENTORY.md](W3-SOURCE-INVENTORY.md).

## Implemented

- Executable intake refuses missing or nonhex execution/code IDs at SealRequest.
  One validator accepts complete SHA-1 and SHA-256 IDs. Credential-bearing source
  intent is refused in its new RequestedRef field as well.
- App webhook intake passes verified SHAs through ResolveRevisions without
  re-reading a branch tip. Source.ForRequest assertions were added to the native
  App fixture for the lead's socket-enabled suite.
- Fresh-branch children resolve in authenticated controller intake against the
  parent's persisted repository and location. The running job never receives
  source credentials. Its original source-credential refusal still passes.
  The real-path proof admits a child, moves the branch, then acquires and
  installs the originally sealed code through fake verified-engine interfaces.
- Schedules and child intake use the same local/remote ref lookup and pair
  resolver. Schedule replay retains both committed IDs when refs move.
- Native assembly no longer classifies the live checkout. The regression test
  selects an older Go pipeline while the current checkout contains TypeScript
  and reaches the older Go engine pin through actual native service assembly.
- Local committed execution and pipeline-code checkouts apply the same submodule
  and LFS materialization as remote checkouts. Eight local/remote/flag combinations
  inspect both distinct checkout roots and preserve the owner's pointer bytes.
- The moved-HEAD test supplies a workspace, verifies acquired HEAD equals the
  sealed old commit and checks that later files are absent. It no longer succeeds
  because acquisition lacked a workspace.
- A Go package ownership guard refuses clone/checkout/worktree command arguments
  outside internal/source. It covers direct, sliced/appended, helper and shell
  argv forms. SDK clone and hook worktrees execute through source/gitcmd. This
  dependency-free leaf avoids the SDK -> source -> secrets -> SDK import cycle.
- Trigger snapshots import a host-owned LocalBundle through shared checkout.
  CLI inspection's remaining direct checkout/worktree commands are deleted.
  Preview's thin temporary-checkout adapter remains for W9 to remove with its
  action_plan.go migration. That file was not edited.
- The unused binary-cache FetchSourceBundle API, its Git importer and raw
  restoration helpers are deleted. Production had no callers to migrate. Upload
  remains. Failed bundle transfers, symlinked pipeline roots, SHA-1 bundles under
  a SHA-256 default, ambient Git controls and cancellation are covered at source.

## Verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and temporary files under /tmp/sw-astra-W3. Tests use
at most four package workers. Whole-repository test execution stays with the
lead harness.

Passed:

- Whole-repository go build ./....
- Focused vet of changed packages and direct dependents.
- Full source, runspec, nodehost, localcontroller, bincache and githooks suites.
  The final source/runspec run took 13.762s/1.628s. The host run took 27.799s.
- Focused controller, client and acceptance regressions, 8.855s/0.028s/0.276s.
- CLI selected-source, snapshot and temporary-checkout checks, 27.995s.
- Go SDK clone/fetch checks, 0.100s.
- Focused race checks for source selection/policy, intake and child scoping passed
  in 10.036s/1.045s/7.621s across source, runspec and controller.
- Source bundle fuzzer: six generated executions with two workers, plus seeds.
- Commentcheck against main: clean across 2,871 files. git diff --check is clean.

The full store suite took 306.922s and exposed six remaining fixture failures.
Their IDs were missing, short, placeholder strings or malformed lengths,
or their second schedule omitted its selected source. All six pass the
final targeted rerun in 1.197s. Their cap, replay, queue, secret-reporting and
retry assertions remain. The repaired names are
TestScheduleAdmissionCountsAllSchedulesAgainstArmedBy,
TestScheduleReplayKeepsFirstSealAfterFollowedRevisionMoves, TestStoreQueueOrder,
TestReportingReadsSealedFactsWithoutSecretValuesProperty,
TestManualRetryPreservesOnlyItsTeamsSealedSourceProperty and
TestManualRetryRetainsVerifiedRepositoryFacts. The complete store suite was not repeated after those
fixture-only edits. The harness must confirm it.

The current Lifecycle, Environment, Credentials and initial-trigger intake
acceptance selections pass. This includes request/plan seals, retry lineage,
secret display classification, setting precedence and origin-HEAD/guard cells.
No acceptance matrix cell was newly enabled.

The full Go SDK Git suite also hit three existing sandbox fixture failures:
TestFilesetHashFilesystemFallback, TestFilesetHashFramesFileContents and
TestFilesetHashIncludesPermissions. These nonrepository fixtures encounter the
sandbox's ancestor /tmp/.git mount. Clone/fetch's focused checks pass. No
unrelated filesystem-hash implementation was changed.

## Ports, contracts and mechanisms

The dead binary-cache importer tests were replaced at the active source owner.
Ports cover exact source bytes and independent workspaces, SHA-1 with a SHA-256
default, internal/absolute-external/relative-external pipeline symlinks, digest
mismatch/short/long streams, close failure, wrong commits, zero downloads after
cancellation, ambient Git controls and cancellation during Git. Upload receipt,
response mismatch and foreign-owner assertions remain in bincache. The removed
raw blob-store budget/Verified-reader assertions belonged to the deleted import
API. Shared signed-source transfer and blob-store boundaries retain their own
checks.

Contract changes made under integration authority are in
[the W3 continuation](contracts/w3-source-selection-continuation.md). C3 now
states that all unaccepted source is classified after acquisition, and separates
execution-host credential release from controller-private ref resolution.
No route, schema, grant, setting, environment variable, cache, retry policy
or timeout was added. The native-source continuation adds the optional
extra_repositories response field for audited submodule authorization. Internal Options.LocalBundle is the new
host-owned snapshot-import input. The new controller adapter and Git leaf are
the mechanisms required by the assigned source-ownership cut.

A client-side ref lookup was rejected because a started node must not receive
source credentials. Resolving inside the existing authenticated intake route
keeps that boundary and the final C1 authority recheck. LocalBundle replaces the
old snapshot's direct checkout. The alternative of dropping snapshots would
remove an accepted feature. No compatibility alias was added for the removed
bundle import API.

## Remaining integration and handoffs

1. W9 owns action_plan.go preview/describe, child local-secret inheritance and
   public docs. Those files/concerns were left untouched. When its migration
   removes the last setupRefWorktree caller, delete this thin adapter too.
2. The TypeScript SDK's user Git clone helper still executes git clone in
   sdk/typescript/src/helpers/git.ts. The guard covers every production Go
   package. This separate SDK runtime does not select pipeline code or engines,
   but a literal cross-language ownership rule needs a lead/W5 decision and
   implementation. It is listed explicitly in the inventory.
3. Live App releases now carry audited ExtraRepositories through C2 and the host
   client. The native-source continuation records its tests and contract changes.
4. W3-native-latest.md is headed 11:56 MDT and contains FAIL results caused by
   missing or placeholder commit IDs. The continuation repairs those fixtures.
   The lead owns the next socket-enabled run and complete repository suite.
5. The previous report's SQLite follower-cancellation DESIGN remains unchanged.
   No newer handoff settled it. This session's full host suite passes.

There is no remaining stub in the new W3 resolver/acquisition implementation.
The engine resolver/installer and remote-public-source access in the child proof
are explicit test doubles. The TypeScript ownership boundary above and native
confirmation remain open, so this report does not claim completion.

## Shared files

- `cmd/sparkwing/run_dispatch.go`
- `cmd/sparkwing/trigger_native.go`
- `internal/acceptance/settings.go`
- `internal/bincache/source_bundle.go`
- `internal/bincache/source_bundle_test.go`
- `internal/bincache/source_bundle_unix_test.go`
- `internal/bincache/source_import.go`
- `internal/bincache/source_import_unix.go`
- `internal/bincache/source_import_windows.go`
- `internal/githooks/fire.go`
- `internal/runspec/burst_test.go`
- `internal/runspec/seal.go`
- `internal/runspec/source.go`
- `internal/runspec/source_test.go`
- `internal/runspec/spec_test.go`
- `internal/wingd/localcontroller/native.go`
- `internal/wingd/localcontroller/native_config_test.go`
- `internal/wingd/localcontroller/node_pipeline_test.go`
- `pkg/controller/child_intake_reconciliation_test.go`
- `pkg/controller/child_source.go`
- `pkg/controller/child_source_test.go`
- `pkg/controller/client/native_services.go`
- `pkg/controller/cycle_detection_test.go`
- `pkg/controller/engine_routes.go`
- `pkg/controller/engine_store_runtime_test.go`
- `pkg/controller/github_app_test.go`
- `pkg/controller/github_app_webhook.go`
- `pkg/controller/intake_policy_test.go`
- `pkg/controller/intake_source.go`
- `pkg/controller/peer_intake_native_test.go`
- `pkg/controller/pipeline_latest_native_test.go`
- `pkg/controller/private_host_test.go`
- `pkg/controller/retry_action_native_test.go`
- `pkg/controller/schedule_routes_test.go`
- `pkg/controller/schedule_source.go`
- `pkg/store/acceptance_plan_seals_test.go`
- `pkg/store/acceptance_queue_order_test.go`
- `pkg/store/app_intake_test.go`
- `pkg/store/intake_plan_runtime_test.go`
- `pkg/store/reporting_seals_test.go`
- `pkg/store/retry_team_test.go`
- `pkg/store/schedule_intake.go`
- `pkg/store/schedule_intake_test.go`
- `pkg/store/schedule_transactions_test.go`
- `pkg/store/source_intake_test.go`
- `sparkwing/git/clone.go`
- `tests/acceptance/trigger_contract_test.go`

W3-owned source and nodehost changes are visible in git diff and the new source
files. Lead-owned handoffs.md and amendments files were read and unchanged.
No operational Sparkwing command, candidate install, Git write, merge, release
or deployment ran. No local Git identity or core.worktree override was left.
Overwing reports its installed binary is ten commits behind source, but standards
matching and reading worked. Scratch is removed after joined checks. The shared
build cache is retained.

Volume: +480/-489, net -9 lines. W3 production versus main: +12423/-26034, net -13611 lines, within the
-2,300-line budget. Counts exclude tests, testdata, acceptance and contract
scaffolding. The deleted production lines include the alternative bundle importer
and direct native/CLI acquisition paths. New source/gitcmd files include the
moved process joining code.

STATUS: CONTINUE
