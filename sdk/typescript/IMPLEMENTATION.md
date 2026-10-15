# TypeScript SDK handoff

W5 implements the merged W4 protocol at the SDK boundary. Native Windows
artifacts and real host/store/placement evidence remain open. The lead owns
commits, pipelines, review and native acceptance.

## This continuation

Both merge conflicts are resolved in the working files. The conformance file
matches the integration branch's v2 runner. The acceptance skeleton retains
both the TypeScript child and W6's configuration checks. Git's index remains
unmerged because SWARM-BRIEF forbids Git writes. The lead must stage the two
resolutions and finish the merge.

The SDK now consumes explicit attempt/settlement state before step predicates
and bodies. One transition validates known unique terminal IDs, complete
transitive prerequisites, active work, terminal fences and attempt ordering
before mutating outputs. New attempts clear prior outputs. Skipped predecessors
have undefined local output. Every body outcome settles before reply, including
failed output encoding and refused dry runs. Job closures carry no step state.

Cancellation accepts a bound job with optional step, or a target request ID.
It aborts and joins selected work before acknowledgement. Original replies
precede that acknowledgement. EOF joins execution before restoring body env.
Rejected foreign selectors cannot cancel bound work. Generator reconstruction
uses the process binding for cancellation and does not apply source body env.

Concurrency scope, capacity, cost and on_limit now preserve omission. Explicit
zero still fails. Only plan requests can supply args. Eval job/step attribution
comes from the registered closure and hello binding. Repeated generator evals
return the recorded local declaration without running author code again.
Generated argument binding fails before any child code is registered.

Annotation and summary requests preserve step attribution. Annotation defaults
to info and preserves end_line. The host applies masking and limits.Record;
the SDK no longer refuses an unmasked record at 128 KiB. Mask registration
errors propagate with the host's safe field diagnostics.

Request and route decoding reject duplicate JSON fields, including escaped
spellings and nested objects. Canonical future error codes remain readable.
Describe failures return pipeline-attributed plan_failed errors. Generated
property cases verify that overlapping secret defaults cannot escape through
message, hint or details.

## Amendments and handoff status

| Entry                 | W5 result                                                                                                                                                                 |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A1-01, A1-02          | Acknowledged mask transport and safe refusal propagation tested. Sink registration and identifier validation belong to W3.                                                |
| A1-03                 | Shared inner-error vectors pass, including duplicate, null and nested-envelope refusals.                                                                                  |
| A1-04, A1-05          | Record routes use the shared payload shape, step attribution and host-owned masked bounds.                                                                                |
| A1-06                 | Job/step and target cancellation join before acknowledgement.                                                                                                             |
| A1-07                 | All four concurrency options retain omission. Shared canonical hashes pass.                                                                                               |
| A1-10                 | The compiled ESM package and package-version hello work under the native host fixture. Selected-engine installation remains W8 integration.                               |
| A1-51                 | Native TypeScript runs the same eight v2 sessions as Go. W4's real parsers, schemas, validator and generated hash vectors judge its output.                               |
| A1-52                 | Native host source cross-compiles for Windows x64 and arm64. Bridge artifacts and native execution remain unprovided. Package preflight refuses the missing x64 artifact. |
| A1-53                 | SDK boundary proofs pass. Native and placement cells remain blocked until exercised by T1/lead.                                                                           |
| A1-54                 | Approval SkipIf remains implemented and tested. Removed approval execution hooks stay absent.                                                                             |
| handoffs.md finding 6 | W5 terminal-state delivery is implemented and tested. W3 must send the same state from its scheduler.                                                                     |

No contract change in another area is needed. Earlier requests for service
routes, cancellation selectors and concurrency presence are resolved by the
merged amendments. No new open design question was introduced.

## Verification

- 141 TypeScript tests pass. Strict consumer/source typechecking passes.
- Native Linux pipes pass, including actual Go request/frame parsing, strict
  plan validation, every eval kind, generated replay/hash parity and child
  handle/credential exclusion.
- The shared corpus sessions pass for describe, plan_once, execution,
  body_failure, hook_failure, unknown_operation, major_mismatch and
  generated_reconstruction.
- The existing TypeScript SDK boundary children pass in all twelve enabled
  scenarios listed by internal/acceptance/typescript.go. Settlement and
  amendment tests now run inside those children. No placement target was
  unskipped.
- Whole-repository go build passes. Focused vet and tests pass for the native
  host, internal/acceptance, tests/acceptance and nodeproto/conformance.
- The native Go host cross-compiles for Windows amd64 and arm64. These are
  compile checks, not native Windows passes.
- Commentcheck, Prettier and git diff --check pass.

Regressions ran red before the settlement/cancellation, describe-diagnostic
and generated-argument fixes. The retained attempt-output, active-settlement,
terminal-fence, dry-run and discovery cases were ported from W4's Go behavior.
The T1 inventory contains no TypeScript-specific source rows.

Reproduce from the SDK directory with TMPDIR under /tmp/sw-astra-W5:

```sh
node --test --experimental-test-isolation=none 'test/**/*.test.ts'
node node_modules/typescript/bin/tsc -p tsconfig.json
```

Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W5. Run go -C with the
absolute worktree path. The temporary directory is deleted after verification.

## Remaining real integration

1. Build and package native/win32-x64/protocol.node and
   native/win32-arm64/protocol.node. Run TestNativeProtocol and
   TestNativeConformance on native Node/Go pairs for both architectures.
   W9 must wire the release build and matching npm/lockfile version stamp.
2. W3 must supply settled snapshots before predicates and bodies, omit attempt
   state from job closures, and consume canonical eval failures. Exercise the
   production service router, startup env and cleanup after SDK death.
3. W1/W2/W7 must exercise authorization, identifier secrecy, masks, artifact
   and memo/depcache persistence, references, child ancestry and OIDC through
   their real services. Fakes establish only the SDK route contract.
4. T1/lead must run every placement, authenticated Git, real Docker/DinD,
   Darwin and native Windows acceptance. W9 owns published schemas/docs.

## Deletions, shared files and friction

No further legacy files were removed. This continuation replaces 116 source
lines and 121 test lines. The previous hard cut removed 2,896 legacy lines,
including preload/startup, run_node, environment bearer transport and v1
SDK documents. Existing helper implementations and public declarations remain.

Shared files touched are internal/nodeproto/conformance/conformance_test.go
and tests/acceptance/skeleton_test.go for merge resolution, plus
internal/acceptance/typescript.go and tests/acceptance/typescript_test.go for
SDK test selection. No caller outside W5 required a production API migration.

The sandbox has no Windows compiler or native Windows runner. Missing bridge
artifacts prevent package completion. Linux and cross-compilation results do
not substitute for native evidence. No Sparkwing pipeline, deployment, release
or Git write was performed. The lead must review and stage the merge resolutions.
