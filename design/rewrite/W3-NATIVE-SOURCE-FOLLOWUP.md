# W3 native source follow-up

This continues the committed publication/identity follow-up at b34f5f875. The
lead's latest native report contains FAIL. W3 remains CONTINUE pending the
lead's commit and next native run.

## Implementation

Invariant: inspection installs the sealed pipeline commit's original bytes while
retaining the resolved execution/code pair, without modifying the user's files.

The native split-source regression failed because preparation made a shallow
execution-only replica, then used it to acquire the separate pipeline commit.
Trigger and burst now give the original checkout and optional captured bundle
to the shared acquisition owner. It acquires both committed workspaces and owns
their cleanup. The submitted request keeps its remote source location.

Deleted snapshotTriggerCheckout and triggerInspectionCheckout (23 production
lines) and their replica setup/origin rewrite. All production changes are +6/-46,
net -40. W3 production remains net -13,510 against main, within its -2,300 budget.
No compatibility surface or new mechanism was added.

The Git-environment property now requires GIT_NO_REPLACE_OBJECTS=1 and verifies
that captured values cannot override it. Existing SDK-selection tests now call
actual trigger inspection instead of rewriting the source location in a test.
The new fuzz property covers committed and captured-working-tree inspection,
dirty contents, a separate code commit, and `git replace <code> <execution>`.

Regenerated the docs mirror through bin/sync-docs.sh. Its drift guard passes.

## Contracts and shared files

Updated w3-source-selection-continuation.md to state that native inspection uses
the original checkout and resolved pair through shared acquisition, with
LocalBundle for snapshots. No wire, schema or public API changed. No additional
cross-area contract change or implementation stub blocks this slice. Earlier W3
amendment/handoff work remains in the committed continuations, including the
committed retry-selection restart test. Lead-owned handoffs/amendments are
unchanged.

Shared production: cmd/sparkwing/{burst_native,trigger_inspect,trigger_native}.go.
Shared tests: cmd/sparkwing/{burst_local_secret_unix,git_environment,
run_source_selection,snapshot_acquisition,trigger_prepare,trigger_sdk,
trigger_worktree}_test.go. These migrate every caller of the deleted helpers.
The test-only checkout fixture calls shared PrepareCheckout and registers
cleanup. Captured-byte, withheld-environment and Git-control coverage remains.
No additional archived test port was needed. Generated shared prose:
pkg/docs/mirror/{deployment,local-execution}.md.

## Verification

Commands use the absolute worktree with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4, TMPDIR=/tmp/sw-astra-W3 and
GOTMPDIR=/tmp/sw-astra-W3/gotmp. Tests use -p 4 at most.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet for cmd/sparkwing, source, procenv, doccheck, controller/client and docs.
- Full source, procenv, doccheck and controller/client suites.
- Original native regressions: TestNativeGitEnvironmentExcludesInheritedControlsProperty
  and TestLocalRunSelectionsReachPreparationSealingAndReplan, plus the new fuzz
  seeds (21.058s). The native split-trigger case previously failed at fetch.
- CLI group (63.360s): go test -p 4 ./cmd/sparkwing -count=1 -run
  '^(TestBurstSource|TestBurstSnapshot|TestBurstSecretScan|TestBurstPreparation|TestTriggerSnapshot|TestTriggerInspection|TestWorkingTreeTrigger|TestSnapshot|TestDefaultBranchLookup|TestProfileCacheBranch|TestLocalEntryPointsWithholdSigningMaterial|TestPrepareTrigger|TestPreparedTrigger|TestTriggerCheckouts|TestTriggerSDK|TestRegisteredNameThroughCLI|TestPipelineAlias|FuzzPreparationGit|FuzzBurstSnapshot|FuzzTriggerInspectionAcquiresOriginalResolvedPair)'.
- Final SDK/inspection group after migrating the SDK test helper (1.622s).
- Five-second fuzz campaign for FuzzTriggerInspectionAcquiresOriginalResolvedPair,
  two workers, four executions. The new property first failed before engine
  selection because inspection tried remote acquisition without source access.
- TestPkgDocsContentMatchesDocsRoot and sync-docs.sh --check.
- Compile-only sparkwing/sparkwingtest and tests/acceptance, both of which the
  native report listed as build failures.
- TestSchedulerRunsADiamondInOrderAndInParallel in isolation (0.251s).
- Commentcheck against main, formatting and diff whitespace checks.

No acceptance skip changed and no native acceptance cell is newly claimed.
The full docs suite cannot bind its httptest listener in this sandbox. Its
socket-dependent tests remain for the lead. The native report's scheduler
interruption did not reproduce. CLI alias failures that reported missing stdlib
packages and cache files also did not reproduce in the focused CLI group.
The controller cursor-fuzz credential failure belongs to W2/W1 and remains in
the native report. No speculative fixes were made for those failures.

Whole-repository tests, socket-dependent native verification, supported-platform
QA and final review remain with the lead. The latest native report must show no
FAIL before COMPLETE. No Git write or Sparkwing candidate/service ran. Scratch
is removed at session end and the shared cache is retained.

STATUS: CONTINUE
