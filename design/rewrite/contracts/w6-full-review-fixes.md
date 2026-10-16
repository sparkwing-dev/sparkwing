# W6 full-review corrections

All 14 findings are fixed. Work remains uncommitted at `cc2524802`. No Git writes, runtime Sparkwing invocation, service rollout or release occurred.

| Finding | Status and invariant | Regression evidence |
| --- | --- | --- |
| 1 | Fixed. Every pipeline-code spawn uses shared credential policy and masked output. Deleted `engineenv` and `nodeEnv`. | Describe stderr and typed protocol errors, npm lifecycle output, credential-name fuzzing, and `TestPipelineSpawnsUseSharedEnvironmentPolicy` ran red then green. Typed errors also survive safe JSON encoding. |
| 2 | Fixed. Credentials recognized by a consumer are registered before spawn. Consumer-specific list splitting and `net/url` parse the actual authority. | Path/query `@`, scheme-less endpoints and escaped userinfo regressions ran red then green. Endpoint fuzzing passed 23,863 executions. |
| 3 | Fixed. Every consumed native environment byte participates in snapshot identity. Digest version 2 hashes sorted byte-preserving entries. | `FuzzEnvironmentDigestBindsNativeBytes` and `TestCapturedEnvironmentNativeBytesCannotReplaceAnotherSnapshot` reproduced collisions/replacement before the fix. Hash fuzzing passed 74,301 executions. |
| 4 | Fixed. The authorized local hook index path reaches checking tools unchanged. It stays in the private captured snapshot and becomes a runtime string path. | `TestSDKGateIndexReachesCheckingTools` first checked the wrong index, then checked a real alternate index for plan/attempt/eval. Remote exclusion and absence of the path from the wire seal pass. |
| 5 | Fixed. Controller construction installs real intake resolution, which reads transactional operator settings and seals preferences/ceilings once. Intake, acceptance and expansion share `store.RequestPlacement`. | Real peer/session admission tests ran red then green. They cover explicit zero, changing persisted operator limits, immutable replay, local/controller/here deadlines, unsafe source refusal, and twelve-hour published history through actual `AcceptPlan` without an executable owner. |
| 6 | Fixed. Accepted concurrency milliseconds fit in `time.Duration`. Both fields reuse the checked conversion. | `TestConcurrencyTimeoutsMustFitDuration` rejected the previously accepted overflow values, while zero and the representable upper boundary pass. |
| 7 | Fixed. Accepted backoff never exceeds execution's shared bound. | `TestRetryBackoffRefusesValuesAboveExecutionBound` ran red then green for five minutes plus one nanosecond and six minutes. Zero and five minutes pass. |
| 8 | Fixed. Enforcement defaults derive from shared typed constants. Event policy is instance-local. | `TestEventDefaultsAreValuesAndPoliciesAreStoreLocal` and existing cache/event/metric/compute enforcement tests pass. Mutable exported event defaults became a value-returning function. |
| 9 | Fixed. Schedule Git discovery registers proxy masks and uses masked diagnostic sinks. | `TestScheduleDiscoverySupportsAuthenticatedProxy` ran red then green, including Git error output. |
| 10 | Fixed. Only an explicit numeric zero disables the cache byte bound. Exact numeric conversion checks positive values before narrowing. | `TestByteBoundDisablesOnlyForExplicitZero` ran red then green for positive sub-byte and underflow spellings. Explicit zero remains accepted. |
| 11 | Fixed. A missing merge field cannot obscure the actual source location. Lookup returns an absence sentinel. | `TestSemanticErrorsLocatePresentMergeSource` ran red then green with an empty first merge mapping. |
| 12 | Fixed. Limits descriptions match storage accounting/eviction, cache pruning and author-value refusal. | `TestTableDocumentsStorageEnforcement` ran red for all three claims, then green. |
| 13 | Fixed. `Read` documents canonical machine-file validation and section decoding. | Public `go doc` assertion against the stale destination-key claim ran red then green. Full userconfig tests pass. |
| 14 | Fixed. The writer no longer promises the removed command or schema sections. | Public `go doc` assertion against the obsolete command/schema prose ran red then green. Full projectconfig tests pass. |

The cuts are complete: native `Setting[T]` run options replace pointer/origin-map round trips; sealed operator winners bypass `distributeOperator`; the unused `Policy` matrix and shape-only test are gone; six trivial named resolver bodies are collapsed; forwarding parser wrappers and six forbidden checkout fields are removed; `engineenv`, the enrolled-key detector and obsolete admission-class hook exports are deleted. Deadline/record tests are consolidated while retaining the separate boundary cases.

`internal/settings` removes 339 net production lines. The pass deletes 960 production lines and adds 557 in W6 paths, for **−403 net production lines** including declaration files. The same six paths are now **+5,486 production lines versus main**, down from the review's +5,889. Outside W6, production changes total +68 lines, so this pass is −335 production lines across the repository. The explicit package deletions include 78 production/146 test lines in `engineenv`, 28 production/18 test lines in the unused policy matrix, 62 net production lines in the enrolled-key path, 5 hook-export lines, and 64 net duplicate deadline/record test lines.

The brief's budget accounting excludes declaration files. Under that definition W6 is **−243 in this pass and +4,442 versus main**, against a −2,395 estimate, an overage of 6,837. This remains a budget exception for the lead. The estimate did not size the full typed precedence/origin tables, two-seal setting and display/reproduction contracts, strict YAML merge/alias validation, and shared environment/limits owners now assigned to these paths. Their old callers and filters were deleted elsewhere. The remaining code implements frozen contracts; this pass removes the duplicate representations rather than hiding the overage through file moves. Further cuts would need a separate reduction of that contract inventory.

Local C4 contract amendments: `Values` embeds native `runspec.Options`; duplicate resource/pause types, `ProjectRequest`, the projection field matrix and inverse projections are removed. `CheckoutLayer` cannot represent the six forbidden fields. Named resolvers remain. Startup inputs, arguments and guards keep separate source metadata. No other-area contract amendment is requested. `store.RequestPlacement` exposes the existing placement rule so intake and acceptance cannot classify a request differently.

Verification passed on the final sources:

- Whole-repository `go build -p 4 ./...` and `go vet -p 4 ./...`.
- Full tests for all six W6 packages, nodehost, capability, pipelineentry and agentconfig.
- All 16 existing W6 `runspec-config` acceptance cells, including final schema and decoder boundaries.
- Focused real controller intake/source tests, store lifecycle/acceptance/expansion/claim tests, CLI local/trigger/schedule tests, and configuration documentation drift tests.
- Commentcheck against main and `git diff --check`.
- Intake-origin fuzzing passed 50,786 executions. Credential-name fuzzing passed 59,775 executions and real describe-error fuzzing passed 4,315.

Checks used `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`, with `GOTMPDIR` under `/tmp/sw-astra-W6/` and test package concurrency at most four. The temporary tree was removed after verification; the shared cache was preserved.

The full protected run has exactly the same three top-level store failures as a separately archived, unchanged HEAD: `TestCollectionBindingCannotChangePayerProperty`, `TestAWarnedCardRepaysTheDebtOnPayNow`, and `FuzzStorageStandingTenantInventory`. The full nodehost and capability suites pass. After the final placement consolidation, focused store lifecycle and controller tests also pass. Full CLI testing stops at `TestResolveSparks_WithManifest_WritesOverlay` because the sandbox denies a loopback socket; the same test reproduces that failure on unchanged HEAD. Relevant CLI package tests pass. Full controller testing also encounters existing cron/bootstrap fixtures and the socket restriction. No additional historical ports were needed; existing W6 ports and acceptance coverage remain active. Pipelines and native service testing remain with the lead under the swarm brief.

Two existing ingress gaps stay explicit and fail closed: verified App delivery payloads are not persisted for transactional lookup, and a remote `gh:N` acquisition identity lacks transaction-safe trusted GitHub facts. This work composes the real settings policy and exercises local, generic remote and publish admission; it does not fabricate App authority or grant repository access from a typed repository name.

Outside W6 files changed (including tests and the source-contract update):

- `cmd/sparkwing/hooks.go`
- `cmd/sparkwing/run_host_test.go`
- `cmd/sparkwing/run_prepare.go`
- `cmd/sparkwing/schedules_repository.go`
- `cmd/sparkwing/schedules_repository_environment_test.go`
- `cmd/sparkwing/trigger_native.go`
- `cmd/sparkwing/trigger_prepare.go`
- `cmd/sparkwing/trigger_prepare_test.go`
- `design/rewrite/contracts/c4-surface-config.md`
- `design/rewrite/contracts/w6-full-review-fixes.md`
- `internal/acceptance/settings.go`
- `internal/agentconfig/agentconfig.go`
- `internal/bincache/prune.go`
- `internal/engineenv/engineenv.go`
- `internal/engineenv/engineenv_test.go`
- `internal/nodehost/cleanups.go`
- `internal/nodehost/cleanups_test.go`
- `internal/nodehost/gate_index_test.go`
- `internal/nodehost/native_snapshot_identity_test.go`
- `internal/nodehost/process_cleanup.go`
- `internal/nodehost/process_environment.go`
- `internal/pipelineentry/environment_test.go`
- `internal/pipelineentry/pipelineentry.go`
- `internal/pipelineentry/pipelineentry_test.go`
- `internal/repos/describe.go`
- `internal/repos/describe_test.go`
- `pkg/controller/intake_policy.go`
- `pkg/controller/intake_policy_test.go`
- `pkg/controller/intake_source.go`
- `pkg/controller/peer_intake_native_test.go`
- `pkg/controller/server.go`
- `pkg/store/compute_limits.go`
- `pkg/store/event_limits.go`
- `pkg/store/event_policy_runtime_test.go`
- `pkg/store/expansion_accept.go`
- `pkg/store/intake_settings.go`
- `pkg/store/metrics.go`
- `pkg/store/plan_contracts.go`
- `pkg/store/plan_seal.go`
- `pkg/store/unit_deadlines_test.go`

STATUS: COMPLETE
