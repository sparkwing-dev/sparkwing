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
| `SPARKWING_WINGD_BIN` | [cli](cli.md), [schedules](schedules.md), [local-execution](local-execution.md) |

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
