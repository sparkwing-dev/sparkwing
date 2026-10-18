# W3 source preparation follow-up

The five findings in W3-review-1792289092-final.md are implemented. The native
report contains no FAIL for the last committed state, 04aa1b377. The lead must
commit this diff and rerun the native suite before this session's changes have
that evidence. Status remains CONTINUE for that handoff.

## Implementation and invariants

1. Preparation never restores withheld local secrets. Approval, capture,
   inspection, baseline lookup, bundle operations and scans require a supplied
   environment. Capture builds `procenv.Git` once and retains it. Internal index,
   object-store and commit-identity overrides extend that environment. The
   ambient merger is deleted. Profile-cache lookups load the request's persisted
   environment. Native trigger inspection also withholds machine-configured
   local secrets before acquisition or SDK installation. Git can execute shell
   callbacks. The old comment claiming otherwise is corrected.
2. Materialization preserves named-reference identity. Replicas import `refs/*`
   into the original namespaces and restore symbolic refs before the common
   checkout-policy finalizer. Tests select a noncurrent local release branch
   and origin/main when original local main points elsewhere. They cover code
   pins, execution selectors and the symbolic default branch through working-tree
   trigger selection. The test fixture removes its preexisting release tag so
   that the local-branch case cannot accidentally test tag lookup.
3. Committed configuration follows contained checkout symlinks. The default
   reader uses `PrepareCheckout` at the selected execution commit, then reads
   through `os.Root`. Selection and acquisition agree for `.sparkwing -> ci`
   with `source: origin/main`, locally and through the remote Git fixture.
   Generated symlink targets cover contained paths, cycles, absolute escapes
   and relative escapes. An escaped configuration cannot be mistaken for an
   absent configuration.
4. Standalone command lines are checked independently of their root or prose.
   Coverage includes the exact multiline serve-start diagnostic, unknown roots,
   shell prompts and bare commands. AST context separates command examples from
   byte-domain constants, commit messages, stdin data and incomplete string
   fragments. The dashboard remedy uses `sparkwing dashboard start`.
5. LocalReplica's comment describes the initial reference import and the common
   checkout-policy finalizer. Existing bundle-history tests still cover depth.

The fsmonitor regression uses a temporary HOME and global core.fsmonitor
callback that prints SIGNING_MATERIAL into an observed file. It first reproduced
the secret leak, then passed after the environment change. The callback must run,
so suppressing Git configuration alone cannot satisfy the assertion. The native
trigger SDK regression also ran red before its fix. Named-ref, symlinked-config
and multiline-command regressions ran red before their implementations.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, and TMPDIR/GOTMPDIR beneath /tmp/sw-astra-W3.
Tests use at most -p 4. Recreate the scratch directory before reproducing.

Passed with go -C followed by the absolute worktree path:

- `build -p 4 ./...` and vet of cmd/sparkwing, internal/source,
  internal/doccheck and internal/web.
- Full internal/source and internal/doccheck suites. The final source run took
  16.444s. The source suite includes selection/acquisition symlink parity and
  bundle-history policy.
- The CLI snapshot, burst-source/secret, working-tree selection, trigger-source,
  inspection, local-run selection and secret-withholding group passed in 71.022s.
  TestLocalEntryPointsWithholdSigningMaterial includes native trigger inspection.
- FuzzPreparationGitNeverRestoresSigningMaterial passed 20 executions with two
  workers. FuzzCommittedConfigurationStaysInsideAcquiredCheckout passed 48
  executions with two workers, including both real external-path seeds.
- Focused controller fresh-child and remote-schedule source tests, nodehost
  acquired-project planning and boundary tests, and web bundle tests passed.
- Commentcheck is clean across 2,902 files. Diff whitespace and Go formatting
  checks pass. No Git writes ran and no fixture identity or core.worktree was
  installed in the worktree's local configuration.

No acceptance cell changed and no acceptance skip was added. No old test port was needed for these
new findings. Whole-repository and socket-dependent native test runs remain with
the lead. The App HTTP fixture, native Windows symlinks and supported-platform
QA have no new evidence from this sandbox session. No implementation stub blocks
this slice.

## Contracts, deletion and shared files

The W3 source-selection continuation records required preparation environments,
reference-preserving import and acquisition-backed committed reads. No other
area's public contract, wire field, setting, schema or environment variable
changed. Lead-owned amendments and handoffs are untouched. Their completed W3
assignments remain covered by the earlier continuations. The accepted masking
residual and earlier follower-cancellation/TypeScript Git ownership handoffs
remain with the lead.

Deleted the 18-line ambient environment merger and replaced the separate
49-line committed-file implementation. No production file was deleted. Across
all touched production files this session is +224/-174, net +50 lines. W3's
production delta against main is -13,645 lines (+12,475/-26,120), below its
-2,300 budget. This count excludes tests, testdata, acceptance, contract
scaffolding and source/sourcetest fixture helpers.

Shared production changes are cmd/sparkwing/{burst_native,burst_source,
local_host,local_run,trigger_native,worktree_snapshot}.go,
internal/doccheck/sourcehints.go and internal/web/bundle.go. Private helper
callers and their tests now pass environments explicitly. Other source edits
belong to W3. The new internal/source/select_test.go must be included in the
lead's commit.

Mechanisms added: one ref-namespace import replaces clone's ref remapping.
The existing acquisition owner replaces the separate committed-file Git path.
That may materialize more source bytes than the old configuration-only partial
fetch, because the same checkout semantics now govern both reads. No new
subsystem or compatibility path was added.

Friction: Git configuration needs quoted callback paths because fuzz seed names
contain '#'. The installed Overwing is ten commits behind its source, but matching
and card reads worked. No candidate installation or Sparkwing runtime was run.
Scratch is removed at session end. The shared build cache is retained.
