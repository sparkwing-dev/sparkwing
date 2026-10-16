# W3 full review fixes

Eight findings are fixed. Finding 2 remains incomplete because its cross-host authority needs a C1/C2/controller contract that does not exist. The local registry improvements below do not close that distributed guarantee.

| Finding | Status | Result |
| --- | --- | --- |
| 1. Sealed secret masks | Fixed | One initialization path registers requested and resolved secret-argument names and both values, including defaults. Fresh owners, resume, claimed execution and evaluation use it before acquisition or SDK startup. Describe resolves the intake/SDK classification union before releasing buffered output. Accepted inputs must match persisted acceptance. |
| 2. Mask acceptance authority | Partial, contract blocked | Every native executor hydrates accepted identifiers from its snapshot. SDK masks, secret/OIDC releases, delegated bearers, describe and environment projection use the registry gate. Environment masks are staged so refusal changes no sink. Registry acceptance uses the shared validator traversal, including caches and references. Separate processes still have separate registries and cannot serialize against owner expansion. |
| 3. SDK result decoding | Fixed | The session boundary selects the result type by operation, uses Reply.Decode, and requires an eval closure kind before transport. Canonical nested errors and verify result shapes are enforced. |
| 4. Evaluation storage | Fixed | Reconstruction carries EvaluationInput.Storage into NodeInput. The nested-generator test reads ancestor output through the supplied storage binding with the fallback callback removed. |
| 5. Cleanup budgets | Fixed | Lifecycle callers drain without a global command timeout. Each registered command owns its two-minute deadline. Production-wrapper tests cover two consecutive exhausted budgets. |
| 6. Default plan cache | Fixed | Shared checkout planning constructs the scoped file cache and fingerprints source, Git metadata, entry, checkout policy, effective environment, arguments, runner, describe and secret registry state. Disabled is explicit. Missing wiring fails. Cache storage errors warn and plan normally. |
| 7. Debug log gate | Fixed | Planning, native ingress and protocol startup filter debug using sealed policy. Actual subprocess tests exercise attempt, evaluation, planning and direct transport. Disabled debug cannot consume the planning buffer. |
| 8. MaskJSON comment | Fixed | The obsolete claim and duplicate JSON normalization are removed. Log attributes use the common MaskJSON implementation. |
| 9. Runner identity fixture | Fixed | The plan no longer injects runner labels through environment. The body reads sparkwing.Runner(ctx), populated by hello. |

The blocker property was written and run before the masking implementation. It failed for resumed and claimed hosts. Generated-value properties now cover acquisition output/errors and actual subprocess stdout, stderr and structured failures across fresh, resumed and claimed entry paths, without manually registering the values. The resumed subprocess property starts the native body from the acquisition callback after owner initialization. It does not claim a full scheduler property. Generated environment collisions cover cache names and referenced pipeline/job names. Identifier and default-classification fuzz tests also pass.

Existing nodehost acceptance-port tests remain green. No acceptance cell was newly unskipped and no additional legacy inventory port is claimed. Invalid hello and failed-result doubles were corrected rather than weakening the strict decoder. The storage reconstruction test no longer supplies the missing production binding through a fallback.

## Contract changes needed

DESIGN: run mask authority. The invariant is that an acknowledged mask and an accepted identifier cannot conflict across placement, process restart or registration source. The existing /node/v1/masks contract is process-local. C1 needs a run-scoped authority that serializes registration with AcceptPlan and AcceptExpansion and retains the protected mask state. C2/controller transport must authorize registration and hydration under the live unit binding. Every executor and log ingress must consume that authority instead of treating a local registry as authoritative. Raw secrets must not enter ordinary records, diagnostics or public snapshots.

The swarm brief says: "If it changes another area's contract, stop that piece and put it under 'Contract changes needed' in your report." Authorization for this shared contract change was requested during the session and remains unanswered. No substitute in-memory or filesystem broker was introduced.

## Verification and scope

- Whole-repository go build ./... and go vet ./... pass.
- Full internal/nodehost, internal/nodeproto and internal/secrets suites pass. A second verbose nodehost run passes too.
- Focused race verification of secret properties, mask registration/acceptance, environment collision checks, cleanup budgets, SDK decoding and log policy passes (3.673 seconds).
- Commentcheck against main and git diff --check pass.
- Nodeproto identifier fuzz passed 127,244 executions. Source fingerprint fuzz passed 1,254 executions. Intake-secret/public-default fuzz passed 4,904 executions.
- The cache's known source inputs include dirty files and Git metadata. External Git directories/alternates, symlink/nonregular dependencies and unresolved executable paths bypass cache reuse. Concurrent external filesystem mutation and arbitrary external SDK dependencies have no determinism guarantee.

Production W3 delta for this session is +375 lines: 522 added and 147 removed. W3's net production delta against main is -14,615 lines, excluding tests, testdata and contract declarations. Shared production changes add 170 and remove 3 lines. Total session production delta is +542. No legacy module remains to delete in this fix scope. The additions implement accepted-mask initialization, staged environment registration, operation-aware decoding and the previously missing default cache assembly.

Files outside W3 production ownership are internal/nodeproto/plan_identifiers.go, plan_identifiers_test.go and plan_validation.go, plus internal/secrets/mask.go. The nodeproto files share the validator's identifier traversal. Masker.Merge transfers already-expanded environment patterns without deriving extra encodings. Documentation changes are design/rewrite/contracts/c3-protocol-sdk.md and this report. No lead-owned handoff or amendment file changed.

No worktree git metadata or history was written. No Sparkwing execution beyond the required read-only info, pipeline, install or release ran. Friction included shared-file compile races, corrected fixture seals/results, the native subprocess fixture losing its race-runtime exit setting, and the missing cross-host authority contract. Every check joined. No nodehost test process remained, and /tmp/sw-astra-W3 was removed. The shared /tmp/sw-gocache was retained.
