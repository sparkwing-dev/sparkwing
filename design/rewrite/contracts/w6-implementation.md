# W6 implementation handoff

W6's libraries and configuration boundaries are implemented. The repository builds with the deleted APIs removed from every caller.

- Intake seals an available App default branch and refuses conflicting branch facts. Origin HEAD can fill only a missing branch.
- Strict project parsing reaches discovery, hooks, schedules, automation, scaffolding and the documentation drift check. Registry metadata preserves explicit empty guards and requirements. Hosted pipelines without YAML entries retain project defaults.
- Machine readers use `runner`, `repos` and `keep_free`. The credential directory survives parsing for the runner owner to consume.
- Deleted ten migration source/test files, totaling 774 lines, plus the obsolete project-profile fallback, legacy doctor/import hooks and migration-specific test functions. This session's tracked diff removes more than 1,300 lines. The prior session's 1,513 removed lines are separate.

## Acceptance and tests

Sixteen W6 boundary cells execute under the matching acceptance scenarios as `runspec-config`:

- `environment-captured-local-base`
- `environment-remote-allowlist`
- `environment-startup-versus-body`
- `environment-text-and-deadline-boundaries`
- `environment-deadline-parent-boundaries`
- `environment-setting-precedence`
- `environment-owner-queue-approval-limits`
- `environment-author-limit-boundaries`
- `environment-origin-head-and-guards`
- `environment-no-progress-versus-execution-timeout`
- `lifecycle-request-and-plan-seals`
- `lifecycle-retry-sealed-lineage`
- `storage-repository-identity`
- `credentials-secret-display-classification`
- `surface-config-decoder-boundaries`
- `surface-config-final-schema`

These cells assert the W6 boundaries. They do not claim transaction, credential, SDK replan or process-placement coverage.

Passed: whole-repository `go build -p 4 ./...`; tests for all six W6 packages, agentconfig, pipelines and acceptance; vet for W6 and the affected caller packages; W6 golangci-lint; commentcheck; focused scaffold, schedule, default-host-config and YAML documentation checks; `git diff --check`.

Go checks use `GOWORK=off GOFLAGS=-buildvcs=false`, with GOCACHE and GOTMPDIR below `/tmp/sw-astra-W6/` and tests bounded to `-p 4`. The lead owns pipelines and broad gates. No git writes or Sparkwing CLI execution occurred. `/tmp/sw-astra-W6/` was deleted after verification. No test process remained in the process sweep.

Ports from the previous session remain in place: PR context, immutable intake, retry lineage, secret display, settings and schedules. Added the W6 default-branch half of `TestHostedRun_ADefaultBranchGuardReadsTheCheckoutsOriginHead`, including conflicting App facts. New regressions cover independent execution/inactivity durations, explicit empty registry overrides, absent pipeline entries, final scaffold YAML and refusal before rewriting invalid project files.

## Real integration left to the owning areas

- W1: call the W6 resolution and sealing functions inside AdmitRun, AcceptPlan and AcceptExpansion transactions. Persist and replay sealed settings and classifications.
- W3/W4: apply procenv at process boundaries, persist captured local/child/schedule snapshots, enforce startup-env plan drift, evaluate guards with acquired origin-HEAD facts, and enforce execution/inactivity deadlines.
- W8: consume `credentials_dir` in the final runner startup path. The existing agent runtime still owns its credential and service setup.
- W9: finish CLI/runtime cutover and regenerate the product reference pages. `Config.Registry` serves existing dispatch metadata consumers, while final accepted settings come from the typed settings contract.
- T1: implement real placement drivers. Their target cells remain skipped and do not count as W6 passes.

A hosted execution probe could not run because binding `127.0.0.1:0` is forbidden in this sandbox. Its file-only defaults regression passes. Native Windows, PostgreSQL, Kubernetes and full product execution remain unverified here.

## Contracts and assumptions

No other area's frozen interface was changed. W6 adds `Config.Registry` to project configuration because existing CLI/host consumers need dispatch metadata from the final schema. It accepts no historical YAML and introduces no alias or fallback parser. Removing it requires migrating those consumers to their final host APIs.

No unresolved W6 design question remains. The integration points above require implementation, not a new contract decision.

## Shared files

- `cmd/sparkwing/action.go`
- `cmd/sparkwing/action_lint.go`
- `cmd/sparkwing/action_new.go`
- `cmd/sparkwing/action_new_test.go`
- `cmd/sparkwing/completion.go`
- `cmd/sparkwing/configure_init.go`
- `cmd/sparkwing/explain_triggers.go`
- `cmd/sparkwing/hooks.go`
- `cmd/sparkwing/init.go`
- `cmd/sparkwing/main.go`
- `cmd/sparkwing/profile_flag.go`
- `cmd/sparkwing/runners.go`
- `cmd/sparkwing/secret_local_test.go`
- `cmd/sparkwing/spark.go`
- `design/rewrite/contracts/c1-data.md`
- `design/rewrite/contracts/c4-surface-config.md`
- `internal/acceptance/seal_boundaries.go`
- `internal/acceptance/settings.go`
- `internal/agentconfig/agentconfig.go`
- `internal/agentconfig/agentconfig_test.go`
- `internal/agentconfig/legacy_migrate.go`
- `internal/crons/arm_test.go`
- `internal/crons/crons.go`
- `internal/crons/crons_test.go`
- `internal/doccheck/yamlcheck.go`
- `internal/doccheck/yamlcheck_test.go`
- `internal/localsecrets/legacy_access_unix_test.go`
- `internal/localsecrets/legacy_access_windows_test.go`
- `internal/localsecrets/legacy_migrate.go`
- `internal/localsecrets/legacy_migrate_test.go`
- `internal/localsecrets/source.go`
- `internal/opsview/doctor.go`
- `internal/opsview/doctor_legacy_settings_test.go`
- `internal/orchestrator/hosted_run.go`
- `internal/orchestrator/local_secrets_test.go`
- `internal/orchestrator/main.go`
- `internal/orchestrator/pod_node_host.go`
- `internal/orchestrator/profile_run.go`
- `internal/orchestrator/project_config_test.go`
- `internal/orchestrator/trigger_host.go`
- `internal/orchestrator/wingd_api.go`
- `internal/orchestrator/wingd_daemon.go`
- `internal/orchestrator/wingd_secrets_legacy.go`
- `internal/profile/legacy_migrate.go`
- `internal/repos/legacy_migrate.go`
- `internal/wingd/budget_resolve_test.go`
- `internal/wingd/legacy_migrate.go`
- `pkg/controller/github_app_automation.go`
- `pkg/controller/github_app_webhook.go`
- `pkg/localws/pipelines.go`
- `pkg/pipelines/execution_model_test.go`
- `pkg/pipelines/guards.go`
- `pkg/pipelines/guards_test.go`
- `pkg/store/default_team_callers_test.go`
- `tests/acceptance/environment_test.go`
- `tests/acceptance/settings_test.go`
- `tests/acceptance/skeleton_test.go`
- `tests/acceptance/surface_test.go`

The report itself is `design/rewrite/contracts/w6-implementation.md`.

STATUS: COMPLETE
