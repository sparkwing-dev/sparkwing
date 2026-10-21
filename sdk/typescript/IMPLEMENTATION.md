# TypeScript SDK review fixes

Findings 1-5 and 7-9 are fixed. Finding 6 has an SDK build, native-test and
package gate. Native Windows artifacts and W9 release-workflow wiring remain
open. This Linux environment cannot establish Windows support.

| Finding                        | Status                                    | Change and regression evidence                                                                                                                                                                                                                                                                                                                                                                                |
| ------------------------------ | ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. Planning secret diagnostics | Fixed                                     | One PlanningDiagnostics owner collects defaults and supplied values before validation. Describe, argument resolution, plan and generation use it. Property tests cover pipeline/job/generated declarations, overlapping secrets, JSON/URI/base64 spellings, keys, nested values and primitive values. Describe shares discovered masks across pipelines.                                                      |
| 2. Frame/output limits         | Fixed                                     | Replies and logs use one checked writer. Only typed step outputs use the 16 MiB output validator. Encoded frames retain the 17 MiB bound. Tests reproduce the oversized log, 256-job declaration and large memo-key evaluation.                                                                                                                                                                               |
| 3. Memo symlinks               | Fixed                                     | One length-prefixed entry encoder distinguishes regular files and link targets without reading target content. Tree roots reject symlink traversal. Shared Go/TS vectors cover entry type, Unicode and byte framing. Filesystem property tests cover target mutation, dangling links and replacement.                                                                                                         |
| 4. Service cleanup window      | Fixed at SDK boundary                     | A random ownership label and host cleanup are established before Docker create. Cleanup can find unacknowledged containers without touching a colliding caller name. Cancellation and generated ownership cases exercise the registered cleanup program against a modeled Docker boundary. Real Docker/host-death acceptance remains lead-owned.                                                              |
| 5. Multi-platform buildx       | Fixed                                     | Load only one requested platform. The replacement test rejects multi-platform --load rather than requiring it.                                                                                                                                                                                                                                                                                                |
| 6. Windows packaging           | SDK path implemented; native/W9 work open | release:native stamps the engine version, builds the native bridge and requires both native protocol/conformance tests without skips. release:pack stamps the assembly checkout and requires both bridges and matching verification records. Prepack checks architecture, version, source digest and bridge digest. Fabricated PE headers no longer pass. No native bridge or Windows test result is claimed. |
| 7. Preload caller              | Fixed                                     | The existing Go caller test failed with the actual two-argument entry. Its replacement executes the prepared Node entry and verifies the protocol argument.                                                                                                                                                                                                                                                   |
| 8. Generated groups            | Fixed                                     | Children inherit all source groups. The same source/child/grandchild group corpus passes through Go and TypeScript.                                                                                                                                                                                                                                                                                           |
| 9. Initialization comment      | Fixed                                     | Both comments guarantee clearing before SDK tools spawn and preserve the independent-import limitation. A documentation-policy regression ran red before the wording changed.                                                                                                                                                                                                                                 |

Each numbered finding had a failing regression before its fix. Finding 7 used
the existing failing caller test. Finding 9 uses a static documentation-policy
check because the defect was the claim in a comment. The Docker load and PE
preflight tests that encoded incorrect success conditions were replaced.

## DESIGN: Windows release packaging

Owner: W9, with native Windows execution supplied by the lead/CI.

Invariant: every release package contains bridges built and natively tested on
both supported Windows architectures for the same engine version and source.

Add required native Windows x64 and arm64 jobs to the release workflow. Each
job installs matching native Node/Go and MSVC, installs SDK dependencies, and
runs `npm run release:native -- --version <tag> --arch <arch> --headers <include/node> --node-lib <node.lib>`.
Upload each `native/win32-<arch>` directory, including its verification record.
The assembly job must use the same source bytes, download both directories and
run `npm run release:pack -- --version <tag> --destination <output-directory>`.
Make publication depend on that assembly job and publish its package. Missing,
skipped or failed native work must block publication. Cross-compilation,
fake handles and PE-header fixtures cannot close this finding.

The SDK commands and prepack refusal are implemented. `.github/workflows/release.yaml`
is outside W5 and still needs that dependency path. No Go/wire contract change
is needed. No other cross-area DESIGN finding was found in this review.

## Mechanisms and assumptions

- The host sanitizes decoded protocol payloads and presentation output. The SDK
  sends generator argument declarations on successful evaluation, so the host
  can register their secret values before releasing gated output.
- The checked frame writer replaces separate reply/log serialization because
  payload length alone excludes envelope and escaping costs. Typed-output
  validation stays separate. There is no new protocol limit.
- Tracked/tree memo digests use Go's memo prefix and byte framing. The prior
  content-only encoding could not distinguish file types. This is a local
  prelaunch key change; the public memoKey API is unchanged.
- Services retain caller-supplied Docker names. Random ownership labels permit
  safe registration before allocation. Registering a caller name for unconditional
  deletion would remove a preexisting container on a create collision.
- Native verification records bind the tested bytes and source to their package.
  Header validation alone does not establish runtime behavior. These records
  are release-build evidence, not a new runtime trust root.

No new public option, environment variable, service or compatibility alias was
added. Removed/replaced production lines: 92. The diff removes the describe-only
masker, post-creation cleanup registration, unconditional multi-platform loading
and generated typed-output checks. No legacy file or public API was deleted.

## Shared files

- `internal/cluster/trigger_typescript_test.go`: port the preload caller test.
- `internal/acceptance/typescript.go`: include diagnostic, native-result and
  initialization-documentation regressions in their existing SDK cells.

All other edits are under `sdk/typescript`. There are no unaddressed SDK contract
questions. Native Windows release integration remains the open dependency above.

## Verification

All Go invocations use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W5/cache, GOTMPDIR=/tmp/sw-astra-W5/tmp and -p 4.
Node tests use TMPDIR and npm caches under the same task directory.

- Whole-repository Go build passes.
- 159 selected TypeScript tests pass. The real-Git integration test is excluded.
- Strict TypeScript source/consumer typechecking passes.
- Linux native protocol/child-inheritance and the eight shared conformance
  sessions pass. Shared Go memo/group vectors pass.
- The ported cluster caller test and vet on nativehost/cluster pass.
- Eleven TypeScript protocol acceptance children and the native-pipe child pass.
  The host-layers child is excluded because it runs the real-Git fixture below.
- Commentcheck with `-base main`, formatting and diff whitespace checks pass.
- Windows native build/test execution and real Docker are not claimed.

Reproduce the SDK suite from its directory:

```sh
node --test --experimental-test-isolation=none --test-skip-pattern='Git helpers clone, fetch and protect tags against a real local remote' 'test/**/*.test.ts' 'test/**/*.test.mjs'
node node_modules/typescript/bin/tsc -p tsconfig.json
```

Reproduce acceptance from the repository root with the Go environment above:

```sh
go test -p 4 ./tests/acceptance -run '^TestProtocol$/[^/]+/typescript' -skip 'TestProtocol/protocol-host-layers-risk-cleanup' -count=1
go test -p 4 ./sdk/typescript/test/nativehost ./internal/nodeproto/conformance -count=1
go test -p 4 ./internal/cluster -run '^TestTriggerLoopPreparesATypeScriptPipelineEntry$' -count=1
```

Friction: no Windows compiler or native Windows runner is available. Default
Node test process isolation failed in this sandbox; disabling test isolation
made the tests runnable. A nested sync-spawn Docker probe was also blocked, so
ownership tests execute the actual registered program against a modeled Docker
process boundary.

An initial broad SDK test run invoked the existing real-Git integration test,
which writes temporary repositories. That exceeded the no-Git-writes instruction.
Its cleanup completed. Subsequent runs excluded it. No worktree Git state was
written, and no Sparkwing binary was executed. The task cache directory is
removed after verification.
