# TypeScript SDK handoff

W5 implements the merged W4 protocol at the SDK boundary. Native Windows
artifacts, placement integration and the diagnostic redesign below remain open.
The lead owns commits, independent review, pipelines and native acceptance.

## This continuation

Closure evaluations now run in request order, matching Go's eval gate. Queued
evaluations register for cancellation before waiting. Cancellation returns
without invoking their code or waiting for the active closure. Canceling a
queued request preserves the order of later requests. Describe and cancellation
remain available while a closure is running. Concurrent generator replay waits
for the first evaluation and returns its recorded declaration. The separate
in-progress generator refusal is deleted.

Protocol mismatch details now use W4's `supported_majors` field. Native hello
verification compares SDK identity with the actual package version.

Package preflight refuses the development version and mismatched versions in
package.json, package-lock.json and the lockfile root package. A temporary-package
test runs the standard npm version command and verifies all three stamps. W9
can use `npm version <engine-version-without-v> --no-git-tag-version` before
packing. Windows bridge binaries remain required.

The shared TestProtocol scenario loop now invokes the TypeScript boundary
runner. Its replacement Go loop had bypassed the call in pendingScenarios.
The twelve SDK children and Linux native-pipe child pass through that loop.
Placement targets remain skipped because SDK tests cannot prove a placement.
The committed merge is resolved. No merge staging remains for the lead.

## DESIGN: planning diagnostic sanitization

Invariant: no declared secret argument appears in a planning error's message,
hint or details.

A generated check used 16 distinct secret defaults with Unicode and regexp
characters at pipeline and job scope. Each plan threw an SDKError containing
the value in message, hint and nested details. All 32 returned errors leaked
it. The prior continuation fixed describe masking. This is a second defect in
diagnostic sanitization, so SWARM-BRIEF's two-strikes rule leaves the redesign
decision with the lead. This continuation does not patch it.

Reproduce by declaring `{name: "token", type: "string", secret: true,
default: value}` in PipelineDef.args or Job.withArgs. Throw
`new SDKError(value, {code: "plan_failed", hint: value,
details: {nested: [value]}})` from plan. Send hello and plan with empty args
through Host from test/helpers.ts. The canonical error contains the value.
The generated reproduction ran under /tmp/sw-astra-W5 and is removed at cleanup.

Proposed redesign: collect nonempty declared secret defaults and supplied values
from pipeline and job specs at one discovery/planning boundary. Use one error
sanitizer for describe, argument resolution and plan failures, covering message,
hint and nested details, including keys. Preserve codes and field paths. Replace
the describe-only replacement with this shared boundary and add a property test
across thrown values, canonical errors, overlapping values, encodings and both
argument scopes. Host masking continues to own execution output and dynamic
service values. This needs no contract change in another area.

## Amendments and handoff status

| Entry                 | W5 result                                                                                                                                                                               |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A1-01, A1-02          | Acknowledged mask transport and safe refusal propagation are tested. W3 owns sink registration and identifier validation.                                                               |
| A1-03                 | Shared strict inner-error vectors pass.                                                                                                                                                 |
| A1-04, A1-05          | Record routes preserve payloads and step attribution. Masking and record bounds stay host-owned.                                                                                        |
| A1-06                 | Job/step and target cancellation join selected requests. Queued eval cancellation now matches Go.                                                                                       |
| A1-07                 | All four concurrency options retain omission. Shared canonical hashes pass.                                                                                                             |
| A1-10                 | Compiled ESM package and package-version hello pass under the native host fixture. W8 owns selected-engine installation.                                                                |
| A1-51                 | The same eight v2 sessions run through native TypeScript and Go. W4 parsers, schemas, plan validation and hash vectors judge TS output. Planning diagnostic secrecy remains open above. |
| A1-52                 | npm version stamping is checked. Native host tests cross-compile for both Windows architectures. The two bridge artifacts and native execution remain missing.                          |
| A1-53                 | SDK children pass in TestProtocol. Native and placement cells remain blocked until T1/lead exercises them.                                                                              |
| A1-54                 | Approval SkipIf is implemented and tested. Removed execution hooks stay absent.                                                                                                         |
| handoffs.md finding 6 | W5 consumes attempt/settlement state before predicates and bodies. W3 still needs to send it from production scheduling.                                                                |

No other area's contract needs changing. All previous W5 hard cuts, declaration
builders, helper implementations, settlement validation and route clients remain
in place.

## Verification

- 144 TypeScript tests pass with zero skips. Strict source/consumer typechecking passes.
- Linux native pipes and all eight shared conformance sessions pass. The tests
  check actual Go parsing, strict plan validation, generated hashes, stream
  separation and child handle/credential exclusion.
- All twelve TypeScript SDK acceptance children pass through TestProtocol.
  Its native-pipe child also passes. No Windows or placement target is claimed.
- Whole-repository Go build, native-host vet, focused Go tests, commentcheck,
  Prettier and git diff checks pass.
- Native host tests cross-compile for Windows amd64 and arm64. Compilation
  does not establish native Windows behavior.

The queued-eval case ports testGoSDKQueuedEvalCancellation. Overlapping replay,
wire detail spelling and manifest stamp regressions also ran red before fixes.
The T1 inventory contains no TypeScript-specific retained source rows.

Reproduce with absolute worktree paths. All Go commands use GOWORK=off,
GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4. GOCACHE, GOTMPDIR and TMPDIR
are under /tmp/sw-astra-W5 and are removed after verification.

```sh
env -C /home/bombuntu/.xwing/envs/1085/sparkwing/sdk/typescript node --test --experimental-test-isolation=none 'test/**/*.test.ts'
env -C /home/bombuntu/.xwing/envs/1085/sparkwing/sdk/typescript node node_modules/typescript/bin/tsc -p tsconfig.json
go -C /home/bombuntu/.xwing/envs/1085/sparkwing test -p 4 ./sdk/typescript/test/nativehost ./internal/nodeproto/conformance -count=1
go -C /home/bombuntu/.xwing/envs/1085/sparkwing test -p 4 ./tests/acceptance -run '^TestProtocol$/[^/]+/typescript' -count=1
```

## Remaining real integration

1. W5/W9 must build and package native/win32-x64/protocol.node and
   native/win32-arm64/protocol.node. Run TestNativeProtocol and
   TestNativeConformance on native Node/Go pairs for both architectures.
   W9 must wire those builds and npm stamping into release packaging.
2. W3 must send settled snapshots before predicates and bodies, omit attempt
   state from job closures and consume canonical eval failures. Exercise the
   production service router, startup env and cleanup after SDK death.
3. W1/W2/W7 must exercise authorization, identifier secrecy, masks, artifacts,
   memo/depcache persistence, references, child ancestry and OIDC through real
   services. SDK route fakes prove only their boundary.
4. T1/lead must exercise all placements, authenticated Git, real Docker/DinD,
   Darwin and native Windows. W9 owns public schema and documentation generation.

## Scope and friction

Only tests/acceptance/protocol_test.go is shared: one call registers the SDK
boundary children. No production caller outside W5 needed migration. No new
legacy file was deleted. Seven obsolete source lines for generator-in-progress tracking and refusal
were removed. Earlier hard cuts removed 2,896 legacy lines.

The eval gate is the only added runtime mechanism. Running all closures
concurrently could not satisfy Go's queued-eval cancellation behavior. The gate
has no public option and can be undone in runner.ts. Version validation extends
the existing package preflight and uses npm's own stamping command.

This environment has no Windows C/MSVC compiler or native runner. Native binaries and
actual Windows package execution cannot be produced here. The lead must resolve
the diagnostic redesign before SDK secrecy parity can be called complete.
No Git write, candidate Sparkwing execution, deployment or release was performed.
