# Environment variables

This reference covers the process environment registry and external environment
reads. The documentation check combines `procenv.Registry` with source reads,
including injected `os.Getenv` calls and environment slices. Registry entries
include emitted runtime values, inherited tool settings and rejected transports.
A variable remains documented while either owner uses or classifies it.

Kinds:

- **configuration**: an operator or user sets it. Each has a flag or config
  equivalent where one exists, and its page says which wins.
- **runtime**: Sparkwing sets it in the environment of pipeline code or a
  job process; pipeline code may read it, and setting it by hand is not
  supported.
- **plumbing**: an internal process input. Some entries still belong to
  execution paths being replaced by the rewrite. Do not set them.
- **test**: only the test suites set it.
- **other tools**: a name another tool defines, which Sparkwing reads as that
  tool documents it.
- **undecided**: an operator knob without a page yet; each one waits on a
  decision to document it or remove it.

The services (`sparkwing-controller` and
`sparkwing-runner`) read their settings from flags and their secrets from
the files in `--credentials-dir`, so no service configuration appears here.


## Configuration: the `sparkwing` CLI and local runs

| Variable | Described in |
|---|---|
| `SPARKWING_CONFIG` | [ci-embedded](ci-embedded.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [profiles](cli-profiles.md) |
| `SPARKWING_HOME` | [architecture](architecture.md), [backup-restore](backup-restore.md), [caching](caching.md), [schedules](schedules.md), [deployment-modes](deployment-modes.md), [diagnosing-admission](diagnosing-admission.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [native-mode](native-mode.md), [sdk](sdk.md), [security](security.md), [versioning](versioning.md), [cli-cache](cli-cache.md), [runners](cli-runners.md), [profiles](cli-profiles.md), [cli-doctor](cli-doctor.md), [cli-queue](cli-queue.md), [cli-runs](cli-runs.md), [cli-secrets](cli-secrets.md), [dashboard](cli-dashboard.md), [cli-version](cli-version.md) |
| `SPARKWING_LOG_FORMAT` | [hooks](hooks.md), [cli-run](cli-run.md) |
| `SPARKWING_SECRETS_KEY` | [backup-restore](backup-restore.md), [git-credentials](git-credentials.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_KEY_FILE` | [machine-config](machine-config.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_PREVIOUS_KEY` | [backup-restore](backup-restore.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_TOOLCHAIN` | [versioning](versioning.md) |

## Configuration: release tooling

| Variable | Described in |
|---|---|
| `SPARKWING_RELEASE_SIGNING_KEY` | [security](security.md) |

## Runtime

The job protocol supplies runtime identity. These names also have consumers in
Sparkwing code. See [node protocol](node-protocol.md).

| Variable | Described in |
|---|---|
| `SPARKWING_JOB_ID` | [node protocol](node-protocol.md) |
| `SPARKWING_RUN_ID` | [architecture](architecture.md) |
| `SPARKWING_GATE_INDEX` | [hooks](hooks.md) |

## Test

| Variable | Why it stays |
|---|---|
| `SPARKWING_TESTLEAK_HOST` | Marks the re-executed test binary in `internal/testleak`; it crosses exec, so it cannot be a flag. |
| `SPARKWING_TEST_PG_URL` | Points the store test suite at a PostgreSQL database. |
| `SPARKWING_TEST_SHELL_BASH` | Names the Git Bash that runs a Windows test's fake `#!/bin/sh` tool; it crosses exec into the stand-in executable, so it cannot be a flag. |
| `SPARKWING_TEST_SHELL_EXE` | Marks the test binary `internal/testshell` re-executes as a Windows test's fake tool; it crosses exec, so it cannot be a flag. |
| `SPARKWING_TEST_STORE` | Selects the store backend the store test suite runs against. |

## Other tools

| Variable | Defined by |
|---|---|
| `AWS_ENDPOINT_URL` | AWS SDK; see [deployment-modes](deployment-modes.md) |
| `AWS_ENDPOINT_URL_S3` | AWS SDK; see [deployment-modes](deployment-modes.md) |
| `GITHUB_REPOSITORY` | GitHub Actions |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry; see [observability](observability.md) |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | OpenTelemetry |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | OpenTelemetry |
| `OTEL_SERVICE_NAME` | OpenTelemetry |
| `OTEL_TRACES_SAMPLER_ARG` | OpenTelemetry |
| `GIT_CONFIG_COUNT` | git |
| `GIT_INDEX_FILE` | git |
| `GIT_SSH` | git |
| `GIT_SSH_COMMAND` | git |
| `SSH_AUTH_SOCK` | OpenSSH agent |
| `GOBIN` | Go toolchain |
| `GOMODCACHE` | Go toolchain |
| `GOPATH` | Go toolchain |
| `GOPRIVATE` | Go toolchain |
| `GOPROXY` | Go toolchain |
| `npm_config_registry` | npm; a runner's dependency proxy setting, which a node process receives |
| `GOWORK` | Go toolchain |
| `PATH` | the operating system |
| `SHELL` | the operating system |
| `LOCALAPPDATA` | Windows |
| `WSL_DISTRO_NAME` | Windows Subsystem for Linux |
| `WSL_INTEROP` | Windows Subsystem for Linux |
| `XDG_CACHE_HOME` | XDG Base Directory specification |
| `XDG_CONFIG_HOME` | XDG Base Directory specification |
| `XDG_RUNTIME_DIR` | XDG Base Directory specification |
| `CI` | CI providers |
| `CLICOLOR_FORCE` | terminal convention |
| `NO_COLOR` | terminal convention |

## Undecided

| Variable | Why it stays |
|---|---|
| `SPARKS_GO_BIN` | The `go` binary the sparks resolver runs when it writes a pipeline's module overlay; nothing sets it. |

## Process environment policy

The registry owns these classifications. A trailing `*` denotes a name family.
Credential and removed-transport rows describe exclusions, not configuration.
Process-specific delivery rules come from `procenv.Policies()`: local pipeline
processes use captured values, remote pipelines use the configured allow list,
and Git receives the credential-free base plus proxy and CA settings.

| Variable | Class | Policy |
|---|---|---|
| `SPARKWING_DEBUG` | Diagnostic | SDK diagnostic output selection. |
| `SPARKWING_RUNNER_NAME` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_RUNNER_TYPE` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_RUNNER_LABELS` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_DRY_RUN` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_BRANCH` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_COMMIT` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_REPO` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_TRIGGER_SOURCE` | RuntimeClass | Composed from the sealed run and granted runner identity. |
| `SPARKWING_TOOLCHAIN_ACTIVE` | Protocol | CLI or test marker; no pipeline inheritance. |
| `SPARKWING_HOOK_SELFTEST` | Protocol | CLI or test marker; no pipeline inheritance. |
| `SPARKWING_TEST_*` | Protocol | CLI or test marker; no pipeline inheritance. |
| `HOME` | Tool | Minimal process base. |
| `TMPDIR` | Tool | Minimal process base. |
| `LANG` | Tool | Minimal process base. |
| `LC_*` | Tool | Minimal process base. |
| `TZ` | Tool | Minimal process base. |
| `USER` | Tool | Minimal process base. |
| `SystemRoot` | Tool | Minimal process base. |
| `ComSpec` | Tool | Minimal process base. |
| `PATHEXT` | Tool | Minimal process base. |
| `TEMP` | Tool | Minimal process base. |
| `TMP` | Tool | Minimal process base. |
| `HTTP_PROXY` | Tool | Remote network and CA base; mask embedded credentials. |
| `HTTPS_PROXY` | Tool | Remote network and CA base; mask embedded credentials. |
| `NO_PROXY` | Tool | Remote network and CA base; mask embedded credentials. |
| `http_proxy` | Tool | Remote network and CA base; mask embedded credentials. |
| `https_proxy` | Tool | Remote network and CA base; mask embedded credentials. |
| `no_proxy` | Tool | Remote network and CA base; mask embedded credentials. |
| `NODE_EXTRA_CA_CERTS` | Tool | Remote network and CA base; mask embedded credentials. |
| `SSL_CERT_FILE` | Tool | Remote network and CA base; mask embedded credentials. |
| `GIT_SSL_CAINFO` | Tool | Git CA bundle path; inherited by source acquisition. |
| `GIT_SSL_CAPATH` | Tool | Git CA directory; inherited by source acquisition. |
| `NODE_OPTIONS` | Tool | Job assignment is always startup environment. |
| `GODEBUG` | Tool | Job assignment is always startup environment. |
| `GOMAXPROCS` | Tool | Job assignment is always startup environment. |
| `TERM` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `TERM_PROGRAM` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `MSYSTEM` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `GOCACHE` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `GONOSUMDB` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `GONOPROXY` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `npm_config_cache` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `PIP_INDEX_URL` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `PIP_TRUSTED_HOST` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `KUBECONFIG` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `OTEL_*` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `AWS_PROFILE` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `AWS_REGION` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `AWS_DEFAULT_REGION` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `DOCKER_HOST` | Tool | Tool-owned; remote pipeline delivery requires operator allow-list unless minimal or network base. |
| `AWS_ACCESS_KEY_ID` | Credential | External credential; never emitted by Build. |
| `AWS_SECRET_ACCESS_KEY` | Credential | External credential; never emitted by Build. |
| `AWS_SESSION_TOKEN` | Credential | External credential; never emitted by Build. |
| `GITHUB_TOKEN` | Credential | External credential; never emitted by Build. |
| `GH_TOKEN` | Credential | External credential; never emitted by Build. |
| `ACTIONS_ID_TOKEN_REQUEST_TOKEN` | Credential | External credential; never emitted by Build. |
| `ACTIONS_ID_TOKEN_REQUEST_URL` | Credential | External credential; never emitted by Build. |
| `SPARKWING_NODE_TOKEN` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_AGENT_TOKEN` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_CACHE_GRANT` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_SOURCE_TOKEN` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_API_TOKEN` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_LEASE_TOKEN` | Credential | Removed transport; forbidden in all Build output. |
| `SPARKWING_NODE_URL` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_PROTOCOL_HANDLES` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_NODE_CLAIM_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_CONTROLLER_URL` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_NODE_ID` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_WINGD_BIN` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_SOURCE_DIR` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_STATE_DB` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_STANDALONE_REASON` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_DEBUG_PAUSE_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_ADMISSION_CLASS` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_WORKERS` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_REPO_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_GITCACHE_URL` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_CACHE_URL` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_PG_URL` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_RERUN` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_RERUN_REFS_DIR` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_CONFIG_ENV` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_FLEET_CONFIG` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_PROFILES` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_REPOS` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_SECRETS` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_START_AT` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_STOP_AT` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_ONLY` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_NO_CACHE` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_REF` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_ALLOW` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_MODE` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_FLEET_PARENT_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_SOURCE_BUNDLE_OBJECT` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `SPARKWING_WORKTREE_BASE_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
| `_SPARKWING_*` | Protocol | Removed engine transport or option; never read as configuration or inherited. |
