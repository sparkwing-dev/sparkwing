# Environment variables

Every environment variable the code reads, what kind it is, and where it is
described. The docs contract test walks the source for environment reads,
including reads through an injected `os.Getenv` and scans of an environment
slice, and fails when a variable is missing from this page, or when this page
names a variable nothing reads.

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


## Runtime: the Jobs and children a runner starts

These inputs still have consumers in execution paths being replaced by the
rewrite. Final credentials travel through private files or protocol hello. See
[node protocol](node-protocol.md) and [runner setup](cli-runners.md).

| Variable | Described in |
|---|---|
| `SPARKWING_AGENT_TOKEN` | [architecture](architecture.md), [auth](auth.md), [local-execution](local-execution.md), [threat-model](threat-model.md) |
| `SPARKWING_CONTROLLER_URL` | [architecture](architecture.md), [auth](auth.md), [security](security.md), [self-hosting](self-hosting.md) |
| `SPARKWING_GITCACHE_URL` | [cache storage](caching.md#storage) |
| `SPARKWING_CACHE_URL` | [architecture](architecture.md), [caching](caching.md) |

## Configuration: the `sparkwing` CLI and local runs

| Variable | Described in |
|---|---|
| `SPARKWING_ADMISSION_CLASS` | [admission](admission.md) |
| `SPARKWING_CONFIG` | [ci-embedded](ci-embedded.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [profiles](cli-profiles.md) |
| `SPARKWING_HOME` | [architecture](architecture.md), [backup-restore](backup-restore.md), [caching](caching.md), [schedules](schedules.md), [deployment-modes](deployment-modes.md), [diagnosing-admission](diagnosing-admission.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [native-mode](native-mode.md), [sdk](sdk.md), [security](security.md), [versioning](versioning.md), [cli-cache](cli-cache.md), [runners](cli-runners.md), [profiles](cli-profiles.md), [cli-doctor](cli-doctor.md), [cli-queue](cli-queue.md), [cli-runs](cli-runs.md), [cli-secrets](cli-secrets.md), [dashboard](cli-dashboard.md), [cli-version](cli-version.md) |
| `SPARKWING_LOG_FORMAT` | [hooks](hooks.md), [cli-run](cli-run.md) |
| `SPARKWING_SECRETS_KEY` | [backup-restore](backup-restore.md), [git-credentials](git-credentials.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_KEY_FILE` | [machine-config](machine-config.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_PREVIOUS_KEY` | [backup-restore](backup-restore.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_TOOLCHAIN` | [versioning](versioning.md) |
| `SPARKWING_WINGD_BIN` | [cli](cli.md), [schedules](schedules.md), [local-execution](local-execution.md) |

## Configuration: release tooling

| Variable | Described in |
|---|---|
| `SPARKWING_RELEASE_SIGNING_KEY` | [security](security.md) |

## Runtime

Read by pipeline code and execution paths. The job protocol defines final runtime
identity. Entries for unported hosts remain here while their consumers exist.

| Variable | Described in |
|---|---|
| `SPARKWING_CACHE_GRANT` | [cache storage](caching.md#storage) |
| `SPARKWING_JOB_ID` | [node protocol](node-protocol.md) |
| `SPARKWING_RUN_ID` | [architecture](architecture.md) |
| `SPARKWING_NODE_ID` | [architecture](architecture.md) |
| `SPARKWING_DRY_RUN` | [sparks-core](sparks-core.md) |
| `SPARKWING_SOURCE_DIR` | [git-credentials](git-credentials.md) |
| `SPARKWING_STANDALONE_REASON` | [local-execution](local-execution.md) |
| `SPARKWING_STATE_DB` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_GENERATION` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_HOLDER` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_LEASE_SECONDS` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_MEMBERSHIP` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_RESERVATION` | [local-execution](local-execution.md) |
| `SPARKWING_GATE_INDEX` | [hooks](hooks.md) |

## Plumbing

These names still have consumers in hosts being replaced by the rewrite. Protocol v2 passes its delegated route bearer in hello and Windows pipe handles in argv. Its final transport is described in [node-protocol](node-protocol.md). This inventory does not make the old environment transport part of v2.

| Variable | Why it stays |
|---|---|
| `SPARKWING_DEBUG_PAUSE_BEFORE` | The retained host reads it to pause before a job. |
| `SPARKWING_DEBUG_PAUSE_AFTER` | The retained host reads it to pause after a job. |
| `SPARKWING_DEBUG_PAUSE_ON_FAILURE` | The retained host reads it to pause after a job failure. |
| `SPARKWING_WORKERS` | Legacy dispatcher concurrency input. Final run worker selection uses shared settings and `--sw-workers`. |

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
| `ACTIONS_ID_TOKEN_REQUEST_TOKEN` | GitHub Actions; see [github-actions-runners](github-actions-runners.md) |
| `ACTIONS_ID_TOKEN_REQUEST_URL` | GitHub Actions; see [github-actions-runners](github-actions-runners.md) |
| `AWS_ENDPOINT_URL` | AWS SDK; see [deployment-modes](deployment-modes.md) |
| `AWS_ENDPOINT_URL_S3` | AWS SDK; see [deployment-modes](deployment-modes.md) |
| `GITHUB_REPOSITORY` | GitHub Actions |
| `GITHUB_RUN_ID` | GitHub Actions |
| `KUBECONFIG` | Kubernetes client configuration |
| `POD_NAMESPACE` | Kubernetes downward API |
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
| `PIP_INDEX_URL` | pip; a runner's dependency proxy setting, which a node process receives |
| `PIP_TRUSTED_HOST` | pip; a runner's dependency proxy setting, which a node process receives |
| `npm_config_registry` | npm; a runner's dependency proxy setting, which a node process receives |
| `GOWORK` | Go toolchain |
| `PATH` | the operating system |
| `SHELL` | the operating system |
| `LOCALAPPDATA` | Windows |
| `MSYSTEM` | MSYS2 and Git for Windows |
| `WSL_DISTRO_NAME` | Windows Subsystem for Linux |
| `WSL_INTEROP` | Windows Subsystem for Linux |
| `XDG_CACHE_HOME` | XDG Base Directory specification |
| `XDG_CONFIG_HOME` | XDG Base Directory specification |
| `XDG_RUNTIME_DIR` | XDG Base Directory specification |
| `CI` | CI providers |
| `CLICOLOR_FORCE` | terminal convention |
| `NO_COLOR` | terminal convention |
| `TERM` | terminal convention |
| `TERM_PROGRAM` | terminal convention |

## Undecided

| Variable | Why it stays |
|---|---|
| `SPARKS_GO_BIN` | The `go` binary the sparks resolver runs when it writes a pipeline's module overlay; nothing sets it. |
