# T1 scenario catalog

<!-- design:begin -->

This is the acceptance coverage map. The [Go scenario registry](../../../internal/acceptance/scenarios.go)
and [boundary scenarios](../../../internal/acceptance/scenarios_boundaries.go) own each case's complete stimuli and primary assertions. The [harness contract](t1-acceptance.md)
defines drivers, fixtures, evidence and activation. The [port inventory](t1-port-inventory.md)
adds required source-test subcases to those scenarios. All product scenarios are skipped skeletons.

The catalog contains 129 scenarios and 2995 applicable cells across
34 targets. These counts describe required coverage, not executed proofs.

Each scenario runs on every listed placement and supported native target unless
its OS, SDK or mode column narrows it. `all` means every corresponding target.
Modes are controller-managed execution and standalone in-process CI. A standalone
cell never supplies evidence for local-controller startup or dashboard installation.
Decision 104 requires native Windows execution for both SDKs; TypeScript has its own targets.

Update this map whenever registry membership, references or applicability change.
`TestCatalogDocumentation` checks those associations. Runtime expectations remain
in the registry and the mapped port cases, not a duplicated prose oracle.

## Lifecycle

Skeleton: `tests/acceptance/lifecycle_test.go`, `TestLifecycle`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `lifecycle-six-placements` | §3.1, §3.2, A6, decision 99 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, processes |
| `lifecycle-request-and-plan-seals` | §3.1, §7.5, A17, A38 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, store, sdk |
| `lifecycle-retry-sealed-lineage` | §3.1, §3.5, A15, decision 98 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, github, store, identities |
| `lifecycle-transition-replay-and-fencing` | §3.3, A7, A8 | C1, C2, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | store, identities, clock |
| `lifecycle-dependency-verdicts` | §3.3, §6.6, A25, A27 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store |
| `lifecycle-generator-barrier` | §3.6, A62 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, clock |
| `lifecycle-generator-terminal-resolution` | §3.6, A62 | C1, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store |
| `lifecycle-generator-authority` | §3.6, §5.1, A58, A62 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, sdk, identities, clock |
| `lifecycle-generator-reconstruct-code` | §3.6, §6.3, A62 | C1, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, processes |
| `lifecycle-completion-revision-race` | §3.9, A64 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, github, clock |
| `lifecycle-finish-path-effects` | §3.3, §3.9, A8, A64 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, store, github, clock |
| `lifecycle-approval-decisions-and-owner-rights` | §3.3, §3.8, §5.1, §6.6, §7.6, A27, A28, A55, A61, A68, decision 99, ch-sdk-protocol §4.1 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, identities, clock |
| `lifecycle-result-commit-atomicity` | §3.3, §3.9, §8.1, A7, A8, A64, A66 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, store, blobs, processes |
| `lifecycle-insertion-cancel-drain` | §3.3, A7, A8 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | store, sdk, clock, processes |
| `lifecycle-generator-group-boundaries` | §3.6, §7.6, A62 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, store, clock |

## Recovery

Skeleton: `tests/acceptance/recovery_test.go`, `TestRecovery`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `recovery-application-retry-ordinal` | §3.3, §4.1, A2, A7 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, processes, clock |
| `recovery-unstarted-lapse` | §3.4, §7.6, A3, A4, A54 | C1, C3, C4 | runner_hosted, launcher, here, github_actions | all | all | all | store, clock, processes |
| `recovery-started-lapse-cap` | §3.4, A3, A4 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, store, clock, processes |
| `recovery-cancel-beats-loss` | §3.3, §3.4, A7 | C1, C2, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | store, clock, processes |
| `recovery-keeper-causes-and-backoff` | §2.4, A5, A9 | C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | clock, processes, identities |
| `recovery-runner-signals` | §2.6, §3.4, A4, A5 | C1, C3 | runner_hosted, github_actions | all | all | all | repo, store, clock, processes |
| `recovery-oom-is-application-failure` | §7.6, A7, A39 | C1, C3, C4 | launcher, runner_hosted | all | all | all | repo, cluster, store, processes |
| `recovery-reconciler-waits-and-reclaims` | §3.3, §3.4, A7 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, clock, processes, repo |
| `recovery-concurrency-continuous-adoption` | §3.7, A60 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, clock, repo, processes |
| `recovery-concurrency-no-revival` | §3.7, A60 | C1, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | store, clock, repo |
| `recovery-controller-ownership` | §3.8, §2.7, A5, A61, A63, decision 88 | C1, C3 | launcher | all | all | all | repo, store, clock, processes |
| `recovery-here-resume-window` | §3.4, §5.1, §9.3, A58, decision 97 | C1, C2, C3, C4 | here | all | all | all | repo, store, identities, clock, processes |
| `recovery-local-orphans` | §2.1, §3.4, A16 | C1, C3, C4 | local, local_triggered | all | all | controller | repo, store, clock, processes, desktop |
| `recovery-ordinal-across-generations` | §3.3, §3.4, §4.1, A4, A7 | C1, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, clock, processes |
| `recovery-keeper-conflict-and-stop-join` | §2.4, §6.5, A5, A9, A26 | C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | clock, processes, catalog |
| `recovery-runner-version-minimum` | decision 102, decision 103 | C3, C4 | runner_hosted, launcher, here, github_actions | all | all | all | versions, processes, store, clock |

## Placement

Skeleton: `tests/acceptance/placement_test.go`, `TestPlacement`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `placement-reserved-pools` | §3.5, A12, A13, A14 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | repo, store, identities |
| `placement-ordered-matching` | §3.2, §3.5, A13, A14 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | store, identities, repo |
| `placement-webhook-trust` | §3.1, §3.5, A15, decision 98 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | github, repo, identities, store |
| `placement-direct-source` | §2.5, §8.4, A19, A72, decision 98 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, blobs, identities, processes |
| `placement-run-host-to-launcher` | §2.3, §3.2, A1 | C1, C2, C3, C4 | runner_hosted, github_actions | all | all | all | repo, sdk, store, cluster |
| `placement-cloud-dind-boundary` | §2.6, §5.4, §5.5, §7.6, A11, A67, decision 89, decision 97, decision 98 | C2, C3, C4 | launcher, runner_hosted | all | all | all | cluster, repo, identities, processes |
| `placement-github-actions-exchange` | §3.2, §5.1, §5.5, A58, A67, decision 99 | C1, C2, C3, C4 | github_actions | all | all | all | github, repo, identities, store |
| `placement-concurrency-scope-and-resources` | §3.2, §3.7, §6.3, §6.6, §7.5, §7.6, A27, A38, A39, A60, decision 99, ch-sdk-protocol §4.1 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, processes, catalog |
| `placement-granted-identity-and-capacity` | §3.5, §5.1, §6.2, A12, A13, A14, A59 | C1, C2, C3, C4 | runner_hosted, launcher, here, github_actions | all | all | all | repo, store, identities, catalog |
| `placement-dind-pod-invariants` | §2.6, §5.4, §5.5, §7.6, decision 89, decision 97, decision 98 | C1, C2, C4 | launcher | all | all | all | cluster, processes, store, clock |

## Credentials

Skeleton: `tests/acceptance/credentials_test.go`, `TestCredentials`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `credentials-delegation-subset` | §5.1, A58 | C1, C2 | local, runner_hosted, launcher, here, github_actions | all | all | all | identities, store, clock |
| `credentials-award-derived-authority` | §5.1, §4.2, A58, A59 | C1, C2 | runner_hosted, launcher, github_actions | all | all | all | identities, store |
| `credentials-bootstrap-exchange` | §5.1, §5.4, A11, A58 | C1, C2, C3, C4 | launcher | all | all | all | identities, cluster, store, processes |
| `credentials-here-award-parent` | §5.1, A58 | C1, C2, C4 | here | all | all | all | identities, store, clock |
| `credentials-matrix-authentication-status` | §5.1, §5.7, A68 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | catalog, identities, store, clock |
| `credentials-matrix-trigger-and-parent` | §5.1, §3.3, A68 | C1, C2 | runner_hosted, here, github_actions | all | all | all | catalog, identities, store |
| `credentials-matrix-node-child-slot` | §5.1, A68 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | catalog, identities, store |
| `credentials-matrix-pipeline-and-audience` | §5.1, §5.5, A68, A48 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | catalog, identities, store |
| `credentials-secret-preflight-and-pools` | §3.5, §5.3, §6.6, A12, A21 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, secrets, identities, store |
| `credentials-secret-identifiers` | §6.3, §3.6, decision 90 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, secrets, store |
| `credentials-source-release-opt-in` | §2.5, §5.1, A19, decision 98 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | repo, identities, secrets, store |
| `credentials-revocation-residuals` | §5.5, §2.7, A63, A67, A72 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | identities, store, clock, blobs, github |
| `credentials-operator-console-split` | §5.1, §9.3, §9.5, A48 | C1, C2, C4 | launcher, runner_hosted | all | all | all | identities, store, catalog |
| `credentials-windows-local-subject` | §5.6, A69, decision 99 | C2, C4 | local, local_triggered, here | windows | all | controller | desktop, identities, processes |
| `credentials-secret-display-classification` | §3.1, §5.1, §6.3, §9.5, A17, A21, A68, decision 90 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | controller | repo, sdk, store, identities, secrets |
| `credentials-holder-mutation-races` | §3.3, §5.1, §8.1, A7, A68, A72 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, identities, clock, blobs, catalog |
| `credentials-secret-identifier-references` | §6.3, decision 90 | C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, secrets, store |
| `credentials-terminal-grace-and-alternatives` | §5.1, §3.3, A68 | C1, C2, C4 | runner_hosted, launcher, here, github_actions | all | all | all | identities, store, clock, blobs |
| `credentials-source-release-races-and-hosts` | §2.5, §5.1, §5.5, A19 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, identities, store, clock, github |

## Storage

Skeleton: `tests/acceptance/storage_test.go`, `TestStorage`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `storage-tenant-schema-and-run-numbers` | §4.1, §4.2, §4.3, A18, A53 | C1 | local, launcher, runner_hosted | all | all | all | store, identities, clock |
| `storage-repository-identity` | §4.1, §3.5, A65 | C1, C2 | local, runner_hosted, launcher, here, github_actions | all | all | all | repo, identities, store |
| `storage-cas-integrity` | §8.1, A66, A72 | C1, C2 | local, runner_hosted, launcher, here, github_actions | all | all | all | blobs, store, identities |
| `storage-reference-edge-laundering` | §8.1, A66 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | blobs, store, identities, repo |
| `storage-hasmany-read-set` | §8.1, A66 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | blobs, identities, store |
| `storage-memo-and-depcache-scope` | §8.1, §5.1, A25, A66 | C1, C2, C3 | local, runner_hosted, launcher, here, github_actions | all | all | all | repo, blobs, identities, store |
| `storage-binary-provenance` | §4.2, §8.1, A71, decision 92 | C1, C2, C3 | local, runner_hosted, launcher | all | all | all | repo, blobs, identities, store, processes |
| `storage-retention-and-newest-success` | §4.4, §8.1, A50 | C1, C3, C4 | local, runner_hosted, launcher | all | all | all | store, blobs, clock, repo |
| `storage-reachability-and-retention-tails` | §4.4, §8.1, A50, A51 | C1, C2 | local, runner_hosted, launcher | all | all | all | store, blobs, clock |
| `storage-log-segments-and-fallback` | §2.4, §2.7, §8.1, A49, A63, A72 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, blobs, store, clock, processes |
| `storage-log-signing-retention-metering` | §4.4, §8.1, A50, A72 | C1, C2, C4 | runner_hosted, launcher, here, github_actions | all | all | all | store, blobs, clock, identities |
| `storage-replica-safe-accounting` | §2.7, §4.2, A51, A63, decision 88 | C1, C2, C4 | launcher, runner_hosted | all | all | all | store, clock, identities |
| `storage-local-remote-cache-opt-in` | §8.5, §8.4, A71, A73, decision 92, decision 93 | C1, C2, C3, C4 | local, local_triggered | all | all | all | repo, blobs, identities, store, processes |
| `storage-publish-one-way` | §3.1, §8.1, §9.3, A6, A71, A73 | C1, C2, C3, C4 | local, local_triggered | all | all | all | repo, blobs, store, identities, processes |
| `storage-memo-materialization-failures` | §3.3, §8.1, A7, A8, A25, A66 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, blobs, clock |
| `storage-owner-receipts-and-stream-verification` | §3.1, §8.1, A66 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | store, blobs, identities |
| `storage-hasmany-large-budget` | §8.1, A66 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | blobs, identities, catalog |
| `storage-quota-settlement-crash-replay` | §2.7, A63 | C1, C2 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | store, blobs, clock, processes |

## Protocol

Skeleton: `tests/acceptance/protocol_test.go`, `TestProtocol`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `protocol-hello-and-handles` | §6.1, §6.2, §5.6, A10, A69 | C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, processes, identities |
| `protocol-version-and-hard-cut` | §6.2, §6.4, A2, A10, A28, A32, decision 97 | C3, C4 | local, runner_hosted, launcher, here, github_actions | all | all | all | sdk, catalog, processes |
| `protocol-strict-plan-parity` | §6.3, §3.2, A27 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, catalog, store |
| `protocol-errors-and-frame-identity` | §6.3, §6.5, §9.2, A24, A26 | C2, C3, C4 | local, runner_hosted, launcher, here, github_actions | all | all | controller | sdk, identities, catalog, processes |
| `protocol-sdk-secrets-mask-annotations` | §6.6, §6.8, A21, A29, A30, A33 | C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, secrets, store |
| `protocol-sdk-artifacts-references-children` | §6.6, §6.8, §8.1, A25, A30 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, blobs, store, identities |
| `protocol-host-layers-risk-cleanup` | §3.1, §6.6, A22, A28 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, repo, store, processes |
| `protocol-go-sdk-fake-host-and-linkage` | §6.7, A31 | C3 | local | all | go | all | sdk, repo, catalog |
| `protocol-typescript-windows-execution` | §6.8, A30, A69, decision 99, decision 104 | C3, C4 | local, local_triggered, runner_hosted, here, github_actions | windows | typescript | all | desktop, sdk, processes |
| `protocol-job-closures-and-skip` | §3.1, §6.3, §6.6, A27, A28, decision 99, ch-sdk-protocol §4.1 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store |
| `protocol-step-failure-policies` | §3.1, §6.3, §6.6, A24, A27, decision 99, ch-sdk-protocol §4.1 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, processes |
| `protocol-strict-plan-boundaries` | §6.3, §7.6, A27 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, catalog, store |
| `protocol-envelope-transcripts` | §6.2, §6.3, §6.5, A10, A26 | C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, processes, catalog |
| `protocol-additive-fields-and-child-handles` | §5.6, §6.2, §6.4, A69, decision 104 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, processes, catalog |
| `protocol-mask-acknowledgement` | §6.6, A29 | C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, secrets, processes, store |
| `protocol-memo-input-boundaries` | §6.6, §8.1, A25 | C1, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, repo, store |
| `protocol-engine-pin-fetch-verify-cache` | §6.4, A32, decision 101, decision 102, decision 103 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | versions, repo, sdk, processes |
| `protocol-signed-version-index-fallback` | decision 102, decision 103 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | versions, clock, processes |
| `protocol-controller-api-twelve-month-window` | §6.4, §9.5, A32, decision 101, decision 102, decision 103 | C1, C2, C3 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | versions, sdk, store, processes |

## Environment

Skeleton: `tests/acceptance/environment_test.go`, `TestEnvironment`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `environment-captured-local-base` | §7.1, §7.3, A34, A36, A37 | C1, C3, C4 | local, local_triggered, here | all | all | controller | repo, sdk, processes, desktop |
| `environment-remote-allowlist` | §7.1, §7.2, §7.3, A19, A37 | C2, C3, C4 | runner_hosted, launcher, github_actions | all | all | all | repo, sdk, secrets, processes |
| `environment-startup-versus-body` | §7.1, A35 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, processes, store |
| `environment-setting-precedence` | §7.5, §9.4, A38 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, catalog |
| `environment-owner-queue-approval-limits` | §7.6, A39, A54, A55, A56 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, clock |
| `environment-author-limit-boundaries` | §7.6, §6.3, A39 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | sdk, repo, store, clock |
| `environment-text-and-deadline-boundaries` | §7.6, A39 | C1, C3, C4 | local, runner_hosted, launcher, here, github_actions | all | all | all | sdk, repo, secrets, store, clock, cluster |
| `environment-origin-head-and-guards` | §3.1, §7.5, §9.4, A15, A38, A43 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store |
| `environment-no-progress-versus-execution-timeout` | §6.6, §7.6, A39, A54, A56, decision 99, ch-sdk-protocol §4.1 | C1, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, store, clock, processes |
| `environment-deadline-parent-boundaries` | §5.1, §7.6, A39 | C2, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | clock, identities, catalog |

## Surface

Skeleton: `tests/acceptance/surface_test.go`, `TestSurface`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `surface-local-controller-idle-lifecycle` | §2.1, A16, A46, decision 91 | C1, C3, C4 | local, local_triggered | all | all | controller | desktop, repo, store, clock, processes |
| `surface-dashboard-stay-install-update` | §2.1, §9.3, A16, A46, decision 91 | C4 | local, local_triggered | all | all | controller | desktop, processes, catalog |
| `surface-schedules-and-managed-timers` | §5.6, §9.3, §9.4, A36, A69, A70, decision 94 | C1, C3, C4 | local_triggered | all | all | controller | desktop, repo, store, clock, processes |
| `surface-standalone-and-clean-start` | §2.1, §4.6, A16, A57, decision 97, decision 99 | C1, C3, C4 | local | all | all | all | repo, desktop, store, processes |
| `surface-cli-vocabulary-and-flags` | §9.1, §9.2, §9.3, A23, A24, A25, A40, A41, A42, A43, A44, A45, A47 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | catalog, repo, store |
| `surface-config-final-schema` | §7.4, §9.4, A20, A37, A38, A43 | C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | catalog, repo, identities |
| `surface-api-and-detail-parity` | §9.5, §9.2, A24, A26, A48 | C1, C2, C4 | local, runner_hosted, launcher, here, github_actions | all | all | controller | catalog, store, identities, repo |
| `surface-launch-qa-real-repositories` | §3.2, §6.8, §9.3, decision 99, decision 100 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | repo, sdk, desktop, github, cluster, catalog, processes |
| `surface-dashboard-browser-job-history` | §2.1, §3.3, §6.6, §8.1, §9.1, §9.5, A16, A24, A26, A29, A33, A46, A49, decision 99 | C1, C2, C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | controller | desktop, repo, sdk, store, secrets, identities, blobs |
| `surface-config-decoder-boundaries` | §7.5, §9.4, A38 | C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | all | catalog, repo |
| `surface-idle-health-probes` | §2.1, A16, decision 91 | C4 | local, local_triggered | all | all | controller | desktop, clock, processes |
| `surface-version-revocation-upgrade-warnings` | decision 101, decision 102, decision 103 | C3, C4 | local, local_triggered, runner_hosted, launcher, here, github_actions | all | all | controller | versions, repo, sdk, desktop, clock |

## Billing

Skeleton: `tests/acceptance/billing_test.go`, `TestBilling`.

| Scenario | Proves | Areas | Placements | OS | SDK | Mode | Fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `billing-award-provenance-immutable` | §4.2, §5.1, §12, A59, decision 100 | C1, C2 | runner_hosted, launcher, here, github_actions | all | all | all | store, identities, clock, stripe |
| `billing-start-award-versus-start` | §2.3, §4.2, §12, A59 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | store, clock, identities, stripe |
| `billing-customer-runner-waits` | §12, A59, decision 100 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | repo, store, clock, identities, stripe |
| `billing-plan-eval-cache-job-minimum` | §3.1, §12, A59, decision 100 | C1, C2, C3 | runner_hosted, launcher, github_actions | all | all | all | repo, sdk, store, clock, stripe |
| `billing-reservation-disruption-replay` | §3.4, §4.2, §12, A4, A59 | C1, C2, C3 | runner_hosted, launcher, here, github_actions | all | all | all | store, clock, processes, stripe |
| `billing-credit-balance-and-admission` | §4.1, §4.2, §12, A52, A59, decision 100 | C1, C2 | runner_hosted, launcher | all | all | all | store, identities, clock, stripe |
| `billing-stripe-checkout-test-keys` | §9.3, decision 100 | C1, C2, C4 | runner_hosted, launcher | all | all | all | stripe, identities, store |
| `billing-stripe-failure-refund-console` | §9.3, §4.4, A50, decision 100 | C1, C2, C4 | runner_hosted, launcher | all | all | all | stripe, identities, store |
| `billing-ledger-survives-retention` | §4.1, §4.4, A18, A50, A52 | C1 | runner_hosted, launcher | all | all | all | store, clock, stripe, blobs |
| `billing-test-to-live-configuration-boundary` | §7.2, §9.3, decision 100 | C2, C4 | launcher, runner_hosted | all | all | all | stripe, identities, catalog, processes |

<!-- design:end -->
