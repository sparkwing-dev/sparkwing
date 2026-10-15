# W4 review 3 fixes

Snapshot against `f61608124`. No git writes or Sparkwing executable runs.

## Findings and evidence

| Finding | Invariant and result |
| --- | --- |
| 1: retry test migration | Application attempts retain one claim. Removed `auto=true`, replaced repeated claims with one host call, decoded v2 `job` requests, and required ordinals `[1, 2, 3]` and one claim generation. BeforeRun refusal must preserve its actual error and leave the body file absent. Removed contradictory retry comments. The original integration test could not reach the scenario because the sandbox refuses its HTTP listener. The ported integration tests still need the lead's run. |
| 2: missing identity test | Every missing engine/runner field must fail before SDK startup, regardless of the remaining values. The old test ran red with `fork/exec /missing/sdk`, confirming that it supplied valid identity. Replaced it with missing engine, name, type and labels cases. All pass. Added a generated-input fuzz property for the same boundary. |
| 3: generator dependency spelling | Original fixture bytes must retain their dependencies through strict v2 validation. The finding's premise is false: C3 protocol notes specify job `deps`/`deps_if_present`; steps use `needs`. Both fixture plans and genfixture's nested expansions pass `ValidatePlan` with their original bytes. Added that process-level regression and dependency assertions. Removed the permissive `decodedJob` helper and constructed the scheduler's recorded fixtures as typed declarations. No wire spelling change. No red reproduction of the alleged spelling defect exists. |
| 4: deleted recovery coverage | Recovery modifiers and nested recovery registrations must target the recovery job. Restored all three execution tests: memo reuse, SkipIf suppression and concurrency refusal, with their fixture shapes. A new fixture check ran red for all three missing shapes, then green after restoration. A separate SDK test exposed a production defect: `JobNode.OnFailure` returned the parent, so modifiers changed the parent and a second registration replaced the first. Returning the recovery job fixes the class and matches Approval.OnFailure. The SDK chain/modifier/closure regression ran red, then green. Execution tests retain their real host path; socket restrictions prevent executing them here. |
| 5: deleted concurrency-key coverage | Distinct scope/identity/group tuples must never share a key, and labels must recover the complete qualifier. Restored scope isolation, separator qualifier and label cases against the live API, plus a fuzz property. Scope isolation passes. Colon collisions and truncated labels reproduce and remain red, owned by W3. DESIGN handoff below. |
| 6: admission fixtures | Author declarations carry scope and group separately; the host adds the fixed box identity. A regression ran red with `box:box-1:box`, then green with `box:box-1:deploys`. Removed key parsing from `planGrouped`, changed all callers to explicit scope, corrected both embedded-key declarations, and supplied `testBox` to run admission. Full admission execution stops earlier in the existing store adapter with `no such table: sparkwing_meta`; it is not a green integration result. |
| 7: capacity documentation | Go capacity zero remains explicit and invalid. Reproducing the documented default ran red at `ValidatePlan`. Corrected the docs and retained zero-rejection/positive-capacity coverage against the actual contract. |
| 8: failure documentation | A recovery receives the supported Stage and Err fields. A probe using the promised Job field failed compilation. Corrected the docs and retained a real recovery-body invocation that checks the verification stage and canonical underlying error. It passes. |

## Local contract amendment

`JobNode.OnFailure`, like `Approval.OnFailure`, returns the newly declared
recovery job. The method signature is unchanged. Its result supports every job
modifier and another recovery registration without restoring a getter. Updated
C3 Go notes, GoDoc and SDK source documentation. Existing callers that need the
parent as a dependency now retain it before registering recovery.

No contract owned by another worker was changed. The two documentation errors
required documentation corrections, not runtime defaults or extra Failure fields.
No production mechanism, compatibility alias, setting or environment variable was added.

## DESIGN: W3 concurrency identity codec

Invariant: encoding a validated `(scope, host identity, group)` tuple is injective,
and its label recovers the entire host identity.

`ConcurrencyKey` concatenates fields with colons; `ScopeLabelFromKey` separately
splits at the first colon. The restored property reproduces `(a, :b)` aliasing
`(a:, b)` as `run:a::b`, and the label tests lose `:with@seps` and `:1@x`.
W3 should own one tuple codec used by admission and all label readers, with
unambiguous field encoding and one decoder. Adding another separator special
case would leave the same class possible. Reproduce with:

```sh
go test -p 4 ./internal/orchestrator -run '^(TestScopeKey|FuzzConcurrencyKeyPreservesIdentity)' -count=1
```

These tests remain active and failing. They were not skipped or weakened.

The previous report's cross-area DESIGN handoffs for production routes,
execution identity, expansion validation, settlement delivery, alias resolution
and environment projection remain the respective workers' responsibility.
This pass does not certify those production paths.

## Verification

Go commands used `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4` where
supported, and `GOCACHE=/tmp/sw-astra-W4/cache GOTMPDIR=/tmp/sw-astra-W4/tmp`.

| Check | Result |
| --- | --- |
| `go build -p 4 ./...` | Pass. |
| `go vet -p 4` on nodeproto, protofd, sdkserve, SDK, nodehost and orchestrator | Pass. |
| nodeproto/conformance, protofd, sdkserve, main SDK, cleanup, docker, inputs and sparkwingtest suites | Pass. |
| Recovery and capacity SDK tests under `-race` | Pass. |
| Missing-identity cases, strict generator transcripts and declaration-key regression | Pass. |
| `FuzzV2SessionRefusesIncompleteIdentity`, two seconds and four workers | Pass, 299 generated executions. |
| Restored recovery fixture validation and scope-isolation case | Pass. |
| `go test -p 4 ./tests/acceptance -run '^TestProtocol/' -count=1` | Pass. Native Linux hello/handles, hard cut, additive fields/child handles, envelope transcripts and Go fake-host/linkage cells pass. Placement skeletons and the persisted memo/production masking boundaries retain their existing skips. No additional placement cell was unskipped. |
| Pipeline corpus parsing | Pass. |
| SDK Git fallback suite | Three failures caused by sandbox `/tmp/.git`; not a product verdict. |
| SDK services suite and host integration checks | Cannot bind sockets in this sandbox. |
| Restored concurrency identity property/label cases | Fail on the W3 defects above. |
| Run admission integration | Fails before admission on the legacy store adapter's missing `sparkwing_meta` table. |
| `.sparkwing` jobs test compilation | Fails on existing removed SDK APIs: Registration, Plan.Nodes, AdmissionClassValue, ResourceHints, Work.StepByID and similar. W9 must port these callers/tests; no shim added. |
| `go run ./internal/commentcheck -base main .` | Two existing W1 violations, state_materialize.go:15 and state_result_records.go:9. No W4 violation. |
| Documentation mirror drift and `git diff --check` | Pass. |

The lead still owns unrestricted host integration, pipeline gates, native
Windows/Darwin evidence, independent review, commits and landing.

## Shared files

Test migrations touch nodehost's v2_migration, generated, run_admission and events
tests, plus its new strict generator transcript test. Orchestrator changes are
the retry tests, envelope fixtures/configuration and restored envelope-recovery
and concurrency-key tests. Shared product documentation is docs/sdk.md and its
generated mirror, synchronized with bin/sync-docs.sh.

Three parent-dependent callers were adjusted for the SDK return contract:
.sparkwing/jobs/example.go, .sparkwing/jobs/integration.go and
internal/pipelinegen/testdata/corpus/verify-teardown/candidate.go.

No source file or test was deleted. Removed 117 tracked lines, including the ten-line permissive fixture
decoder, obsolete retry loops/assertions/comments and the old OnFailure doc block.
The restored coverage is active; unresolved production failures are reported above.

## Open questions and friction

The request ended after “add a DESIGN:”; no clarification arrived. Cross-area
design findings are recorded here with an invariant, reproduction and proposed
owner-level change. The review's job dependency spelling claim contradicted both
the contract and the executable validator. Socket restrictions, the legacy store
adapter and the stale pipeline SDK tests prevent a full host verdict.
