# W3 native admission continuation

The failures listed in the 12:15 MDT native report pass focused checks here.
Executable intake still requires resolved execution and code commits.
The lead must rerun the native suite before W3 can be complete.

## Implementation and source inventory

The [complete source inventory](W3-SOURCE-INVENTORY.md) was refreshed before edits
at b4662bde5. It lists acquisition, checkout, snapshot, materialization and
SDK/engine selection paths, including W9's preview/describe and the TypeScript
SDK job helper. The Go acquisition ownership guard passes. The new test-only
SDK dependency capture is also listed there.

The committed source-selection work remains in place. SealRequest refuses
unresolved executable identities. App webhook and fresh-branch child intake
resolve before sealing. Native services defer unaccepted SDK classification to
Acquire. Local and remote committed checkouts share submodule/LFS policy.
The moved-HEAD test checks the actual acquired HEAD and source bytes.

This continuation repairs the remaining native fixtures:

- Local-controller admission supplies Source.Ref from the acquired commit for
  SHA-1 and SHA-256. Both identities survive admission and replay. The unborn
  checkout remains inspectable, but executable intake returns invalid_args and
  a subsequent real controller list contains no run. This follows the required
  commit-identity gate without inventing an ID.
- Receipt and secret-display fixtures now carry the code commit alongside the
  execution commit. Their receipt, retry and redaction assertions remain.
- The burst SDK fixture captures tracked working bytes into an owned dependency
  directory. Its local SDK replacement no longer points at the live repository,
  where concurrent TypeScript tests create and delete .command-* files.
  Acquisition and execution still use the real source/engine path in all three
  placements.

Invariant: a burst proof's captured SDK build inputs do not include another
test's transient files. The native error was reproduced deterministically by
placing a dangling transient protocol path under sdk/typescript/test/.command-W3-*.
Before the fixture change, all three placements failed with the same missing-file
install error. With the same probe, all three pass. The probe was removed.
Production cache errors still fail loudly.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3 and TMPDIR=/tmp/sw-astra-W3.
Go commands used -C /home/bombuntu/.xwing/envs/1081/sparkwing. Tests used -p 4.

Passed:

- Whole-repository go build ./... and go vet of ./cmd/sparkwing and ./pkg/controller.
- TestProductionLocalControllerAdmitsRun, TestReceiptEndpoint_RoundTrip,
  TestSecretArgs_* and TestSecretArgsPublicReadsProperty. The regular run took
  1.140s for cmd/sparkwing and 1.319s for pkg/controller.
- The burst transient-file repro passed in 6.743s after failing in 3.853s before
  the fix. TestBurstUploadedSDKBuildsAndExecutesEveryAward covers here, personal
  and cloud awards through real SDK compilation and execution.
- Race runs of the repaired admission, receipt, secret-display and burst tests.
  cmd/sparkwing took 12.063s and pkg/controller took 38.244s.
- Source ownership guard and its detector, local/remote checkout-policy parity,
  moved-HEAD behavior, verified-commit pass-through, executable-seal property and
  native SDK-classification regression. Source took 5.505s, runspec 0.008s and
  localcontroller 0.019s.
- Fresh-child source-scope property, real intake-to-acquisition child test and
  the intake source-boundary property. The controller selection took 0.977s.
- Existing Lifecycle, Environment and Credentials acceptance components, 0.152s.
  Passing cells include lifecycle-request-and-plan-seals,
  lifecycle-retry-sealed-lineage, environment-origin-head-and-guards and
  credentials-secret-display-classification. Placement cells remain skipped.
- Commentcheck against main, clean across 2,871 files. Diff whitespace is clean.

No acceptance cell was newly enabled, no archived test was newly ported and no
failure was converted to a skip. The whole repository suite and socket-dependent
native tests belong to the lead harness. W3-native-latest.md still contains FAIL
and reflects the prior committed state. These working-tree checks cannot replace
that completion gate.

## Contracts, shared files and volume

No contract, route, wire field, credential, schema, runtime setting or environment
variable changed. No API was removed and no production caller needed migration.
No new cross-area contract change is needed for these fixture repairs.

Shared test files changed:

- cmd/sparkwing/local_controller_policy_test.go
- pkg/controller/burst_sdk_source_test.go
- pkg/controller/receipts_test.go
- pkg/controller/secret_args_display_test.go

The inventory and this report are W3 artifacts. W9's action_plan.go,
preview/describe, child local-secret inheritance and public docs were untouched.
Lead-owned handoffs/amendments were read and left unchanged.

This session adds 56 test lines and deletes 3, net +53. Production change is zero.
W3's production delta against main is -13,614 under the lead's area exclusions,
within the -2,300 budget. Prior legacy source import and direct-checkout deletions
remain deleted.

Mechanism added: one test helper copies tracked working bytes and preserves file
modes and symlinks into an owned SDK module. It is needed because the live local
replacement made the proof depend on concurrent tests' temporary files. Bypassing
the binary cache would weaken coverage. Undo is to remove the helper and restore
the live replacement. No production mechanism was added.

## Remaining integration and friction

The lead must commit these explicit files and run its full repository/native
checks. No source-selection stub remains. W9's source preview migration remains
its concurrent slice. The TypeScript SDK job-clone ownership question and the
previous SQLite follower-cancellation DESIGN remain as recorded in the earlier
W3 reports. No newer handoff settles that cancellation design.

No new policy question was raised. The strict executable gate also applies to
unborn local checkouts. A future pre-commit execution feature would need a real
resolved snapshot identity before intake.

The sandbox prevents native socket coverage. Overwing reports ten commits of
binary/source drift, but standards matching and reading succeeded. No Git write,
operational Sparkwing command, release or deployment ran. All checks joined before
cleanup. /tmp/sw-astra-W3 was deleted and /tmp/sw-gocache was retained.

STATUS: CONTINUE
