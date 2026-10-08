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
  node process; pipeline code may read it, and setting it by hand is not
  supported.
- **plumbing**: Sparkwing sets it for a process it starts itself, usually the
  compiled pipeline binary. It crosses an exec, and the binary on the other
  side may be built from an older SDK, so it stays an environment variable
  rather than a flag. Do not set it.
- **test**: only the test suites set it.
- **other tools**: a name another tool defines, which Sparkwing reads as that
  tool documents it.
- **undecided**: an operator knob without a page yet; each one waits on a
  decision to document it or remove it.


## Configuration: `sparkwing-controller`

| Variable | Described in |
|---|---|
| `SPARKWING_BILLING_URL` | [auth](auth.md) |
| `SPARKWING_CACHE_BLOB_STORE` | [self-hosting](self-hosting.md) |
| `SPARKWING_CLOUDFRONT_DOMAIN` | [self-hosting](self-hosting.md) |
| `SPARKWING_CLOUDFRONT_KEY_PAIR_ID` | [self-hosting](self-hosting.md) |
| `SPARKWING_DASHBOARD_URL` | [hooks](hooks.md) |
| `SPARKWING_DEFAULT_PREFER_LABELS` | [scheduling](scheduling.md) |
| `SPARKWING_EMAIL_CONFIGURATION_SET` | [auth](auth.md) |
| `SPARKWING_EMAIL_SENDER` | [auth](auth.md) |
| `SPARKWING_EXTERNAL_URL` | [hooks](hooks.md) |
| `SPARKWING_GITHUB_APP_ID` | [github-app](github-app.md) |
| `SPARKWING_GITHUB_APP_SLUG` | [github-app](github-app.md) |
| `SPARKWING_GITHUB_CLIENT_ID` | [auth](auth.md) |
| `SPARKWING_GOOGLE_CLIENT_ID` | [auth](auth.md) |
| `SPARKWING_LOGS_ARCHIVE_STORE` | [self-hosting](self-hosting.md) |
| `SPARKWING_METRICS_ADDR` | [observability](observability.md) |
| `SPARKWING_OAUTH_REDIRECT_URIS` | [auth](auth.md) |
| `SPARKWING_OBJECT_STORE_BUCKET_MEASURE_PAGES` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_URL` | [observability](observability.md) |
| `SPARKWING_OPERATOR_ACCOUNTS` | [auth](auth.md) |
| `SPARKWING_REQUIRE_AUTH` | [security](security.md) |
| `CACHE_POD_URL` | [gitcache](gitcache.md) |
| `SPARKWING_CACHE_URL` | [architecture](architecture.md), [caching](caching.md) |
| `SPARKWING_CONTROLLER_EGRESS_DAILY_ALARM_BYTES` | [observability](observability.md) |
| `SPARKWING_CONTROLLER_EGRESS_MAX_DOWNLOADS` | [observability](observability.md) |
| `SPARKWING_CONTROLLER_EGRESS_MAX_LOG_STREAMS` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_PUT_PER_MINUTE` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_PUT_PER_DAY` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_GET_PER_MINUTE` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_GET_PER_DAY` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_LIST_PER_MINUTE` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_LIST_PER_DAY` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_DELETE_PER_MINUTE` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_DELETE_PER_DAY` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_BREAKER` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_TRIP_RESET` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_MAX_BUCKET_OBJECTS` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_WARN_BUCKET_BYTES` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_WARN_BUCKET_OBJECTS` | [observability](observability.md) |
| `SPARKWING_OBJECT_STORE_BUCKET_RECONCILE` | [observability](observability.md) |

## Runtime: the Jobs and children a runner starts

Sparkwing sets these for a Job, a trigger's `handle-trigger` child or a node
process; the runner itself reads its settings from flags. The `sparkwing` CLI
also reads the URLs as a fallback for its own `--controller` and `--logs`.

| Variable | Described in |
|---|---|
| `SPARKWING_AGENT_TOKEN` | [architecture](architecture.md), [auth](auth.md), [local-execution](local-execution.md), [threat-model](threat-model.md) |
| `SPARKWING_CONTROLLER_URL` | [architecture](architecture.md), [auth](auth.md), [security](security.md), [self-hosting](self-hosting.md) |
| `SPARKWING_LOGS_URL` | [architecture](architecture.md) |
| `SPARKWING_GITCACHE_URL` | [caching](caching.md), [gitcache](gitcache.md), [self-hosting](self-hosting.md) |

## Configuration: the `sparkwing` CLI and local runs

| Variable | Described in |
|---|---|
| `SPARKWING_ADMISSION_CLASS` | [admission](admission.md) |
| `SPARKWING_ALLOW_UNADMITTED` | [cli](cli.md), [local-execution](local-execution.md) |
| `SPARKWING_ARTIFACT_DIGEST_BACKFILL` | [caching](caching.md) |
| `SPARKWING_BUDGET` | [local-execution](local-execution.md) |
| `SPARKWING_CACHE_MAX_BYTES` | [caching](caching.md), [cli-cache](cli-cache.md) |
| `SPARKWING_CACHE_MAX_ENTRIES` | [caching](caching.md), [cli-cache](cli-cache.md) |
| `SPARKWING_CONFIG` | [ci-embedded](ci-embedded.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [cli-configure](cli-configure.md), [cli-fleet](cli-fleet.md) |
| `SPARKWING_CONFIG_ENV` | [machine-config](machine-config.md) |
| `SPARKWING_DEBUG` | [sdk](sdk.md) |
| `SPARKWING_DEV_ENV_DISABLE` | [architecture](architecture.md) |
| `SPARKWING_FLEET_CONFIG` | [machine-config](machine-config.md) |
| `SPARKWING_HASH_ALL_FILES` | [caching](caching.md) |
| `SPARKWING_HOME` | [architecture](architecture.md), [backup-restore](backup-restore.md), [caching](caching.md), [crons](crons.md), [deployment-modes](deployment-modes.md), [diagnosing-admission](diagnosing-admission.md), [local-execution](local-execution.md), [machine-config](machine-config.md), [native-mode](native-mode.md), [sdk](sdk.md), [security](security.md), [versioning](versioning.md), [cli-cache](cli-cache.md), [cli-cluster](cli-cluster.md), [cli-configure](cli-configure.md), [cli-doctor](cli-doctor.md), [cli-fleet](cli-fleet.md), [cli-queue](cli-queue.md), [cli-runs](cli-runs.md), [cli-secrets](cli-secrets.md), [cli-serve](cli-serve.md), [cli-version](cli-version.md) |
| `SPARKWING_LOG_FORMAT` | [hooks](hooks.md), [cli-run](cli-run.md) |
| `SPARKWING_LOGS_DROP_POLICY` | [observability](observability.md) |
| `SPARKWING_NO_AUTO_REGISTER` | [cli-configure](cli-configure.md) |
| `SPARKWING_NO_BINCACHE` | [caching](caching.md) |
| `SPARKWING_NO_SPARKS_RESOLVE` | [ci-embedded](ci-embedded.md) |
| `SPARKWING_PAUSE_TIMEOUT` | [cli-debug](cli-debug.md) |
| `SPARKWING_PROFILES` | [machine-config](machine-config.md) |
| `SPARKWING_REPOS` | [machine-config](machine-config.md) |
| `SPARKWING_RERUN_IMAGE` | [cli-debug](cli-debug.md) |
| `SPARKWING_S3_ENDPOINT` | [deployment-modes](deployment-modes.md), [observability](observability.md), [self-hosting](self-hosting.md) |
| `SPARKWING_SECRETS` | [machine-config](machine-config.md) |
| `SPARKWING_SECRETS_KEY` | [backup-restore](backup-restore.md), [git-credentials](git-credentials.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_KEY_FILE` | [machine-config](machine-config.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_SECRETS_PREVIOUS_KEY` | [backup-restore](backup-restore.md), [machine-config](machine-config.md), [security](security.md), [cli-secrets](cli-secrets.md) |
| `SPARKWING_TOOLCHAIN` | [versioning](versioning.md) |
| `SPARKWING_VERSION_HOLD` | [cli-version](cli-version.md) |
| `SPARKWING_WINGD_BIN` | [cli](cli.md), [crons](crons.md), [local-execution](local-execution.md) |
| `SPARKWING_BOX_ID` | [sdk](sdk.md) |
| `SPARKWING_CACHE_TOKEN` | [gitcache](gitcache.md), [local-execution](local-execution.md), [self-hosting](self-hosting.md) |
| `SPARKWING_SUBMIT_ENV_ALLOW` | [cli-run](cli-run.md) |
| `TYPESAFE_API_KEY` | [admission](admission.md) |

## Configuration: release tooling

| Variable | Described in |
|---|---|
| `SPARKWING_RELEASE_SIGNING_KEY` | [security](security.md) |

## Runtime

Set by Sparkwing for pipeline code and node processes.

| Variable | Described in |
|---|---|
| `SPARKWING_RUN_ID` | [architecture](architecture.md) |
| `SPARKWING_NODE_ID` | [architecture](architecture.md) |
| `SPARKWING_RUNNER_NAME` | [architecture](architecture.md) |
| `SPARKWING_RUNNER_TYPE` | [architecture](architecture.md) |
| `SPARKWING_RUNNER_LABELS` | [architecture](architecture.md) |
| `SPARKWING_API_SOCKET` | [architecture](architecture.md) |
| `SPARKWING_PARENT_LIVENESS_FD` | [architecture](architecture.md) |
| `SPARKWING_PRIORITY` | [sdk](sdk.md) |
| `SPARKWING_DRY_RUN` | [sparks-core](sparks-core.md) |
| `SPARKWING_CACHE_GRANT` | [gitcache](gitcache.md) |
| `SPARKWING_SOURCE_DIR` | [git-credentials](git-credentials.md) |
| `SPARKWING_PIPELINE_REV` | [local-execution](local-execution.md) |
| `SPARKWING_RUN_HANDLE_FILE` | [local-execution](local-execution.md) |
| `SPARKWING_STANDALONE_REASON` | [local-execution](local-execution.md) |
| `SPARKWING_STATE_DB` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_GENERATION` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_HOLDER` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_LEASE_SECONDS` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_MEMBERSHIP` | [local-execution](local-execution.md) |
| `SPARKWING_NODE_CLAIM_RESERVATION` | [local-execution](local-execution.md) |
| `SPARKWING_TOOLCHAIN_ACTIVE` | [versioning](versioning.md) |
| `SPARKWING_FLEET` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_PARENT_GUARD` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_PARENT_TOKEN` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_BUNDLE` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_BUNDLE_BYTES` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_BYTES` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_FILES` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_MANIFEST_DIGEST` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_REPO_URL` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_ROOT` | [local-execution](local-execution.md) |
| `SPARKWING_FLEET_SOURCE_SHA` | [local-execution](local-execution.md) |
| `SPARKWING_GATE_INDEX` | [hooks](hooks.md) |
| `SPARKWING_ALLOW` | [cli](cli.md) |
| `SPARKWING_START_AT` | [security](security.md) |

## Plumbing

| Variable | Why it stays |
|---|---|
| `SPARKWING_BINARY_SOURCE` | `sparkwing` sets it when it runs the compiled pipeline binary, which records in the run's invocation where that binary came from. |
| `SPARKWING_BROKERED_ARTIFACTS` | Set to `1` for a remote node's child process when its artifacts go through the parent's broker. |
| `SPARKWING_BROKERED_NODE_CLAIM` | Set to `1` for a remote node's child process when the parent holds a node claim, so the child runs as that claim. |
| `SPARKWING_EXECUTION_CAPABILITY_STDIN` | Set to `1` for a remote node's child process, which then reads its execution capability from stdin, keeping it off argv and out of the environment. |
| `SPARKWING_CHILD_LEASE_TOKEN` | The admission daemon's lease for a child run, handed across exec. |
| `SPARKWING_LEASE_TOKEN` | The admission daemon's lease for this run, handed across exec. |
| `SPARKWING_DEBUG_PAUSE_BEFORE` | `sparkwing debug` sets it for the pipeline binary: pause before the named step. |
| `SPARKWING_DEBUG_PAUSE_AFTER` | `sparkwing debug` sets it for the pipeline binary: pause after the named step. |
| `SPARKWING_DEBUG_PAUSE_ON_FAILURE` | `sparkwing debug` sets it for the pipeline binary: pause when a step fails. |
| `SPARKWING_LOCAL_ONLY` | Carries `--sw-local-only` to the pipeline binary. |
| `SPARKWING_LOG_LEVEL` | Carries `--sw-verbose` (`-v`) to the pipeline binary as `debug`. |
| `SPARKWING_MASK_VALUES_FD` | The inherited pipe the pipeline binary writes registered secret values to, so the launcher masks them in the child's stdout and stderr. |
| `SPARKWING_NO_CACHE` | Carries `--sw-no-cache` to the pipeline binary. |
| `SPARKWING_NO_UPDATE` | Carries `--sw-no-update` to the pipeline binary, which records it with the run's invocation. Setting it by hand changes only that record; `SPARKWING_NO_SPARKS_RESOLVE=1` is the knob that skips sparks resolution. |
| `SPARKWING_ONLY` | Carries `--sw-only` to the pipeline binary. |
| `SPARKWING_PROFILE` | Carries `--profile` to the pipeline binary. |
| `SPARKWING_REF` | Carries `--sw-ref` to the pipeline binary. |
| `SPARKWING_SECRETS_PROFILE` | Carries `--sw-secrets` to the pipeline binary. |
| `SPARKWING_STOP_AT` | Carries `--sw-stop-at` to the pipeline binary. |
| `SPARKWING_NODE_SPEC_HASH` | Fences a claimed node to the plan it was claimed under, across the exec into the node process. |
| `SPARKWING_MODE` | Carries `--sw-mode` to the pipeline binary. |
| `SPARKWING_WORKERS` | Carries `--sw-workers` to the pipeline binary. |

## Test

| Variable | Why it stays |
|---|---|
| `SPARKWING_TESTLEAK_HOST` | Marks the re-executed test binary in `internal/testleak`; it crosses exec, so it cannot be a flag. |
| `SPARKWING_TEST_PG_URL` | Points the store test suite at a PostgreSQL database. |
| `SPARKWING_TEST_STORE` | Selects the store backend the store test suite runs against. |

## Other tools

| Variable | Defined by |
|---|---|
| `ACTIONS_ID_TOKEN_REQUEST_TOKEN` | GitHub Actions; see [github-actions-runners](github-actions-runners.md) |
| `ACTIONS_ID_TOKEN_REQUEST_URL` | GitHub Actions; see [github-actions-runners](github-actions-runners.md) |
| `GITHUB_REPOSITORY` | GitHub Actions |
| `GITHUB_RUN_ID` | GitHub Actions |
| `KUBECONFIG` | Kubernetes client configuration |
| `POD_NAME` | Kubernetes downward API |
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
| `GOWORK` | Go toolchain |
| `PATH` | the operating system |
| `SHELL` | the operating system |
| `USER` | the operating system |
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
| `SPARKWING_DISPATCH_WAIT_TIMEOUT` | Bounds how long a run waits for a dispatch; an earlier changelog entry tells users to set it to `off`. |
| `SPARKWING_STORE_WEDGE_BUDGET` | Bounds how long a wedged store call may block; only tests set it. |
| `SPARKWING_SQLITE_BUSY_TIMEOUT_MS` | SQLite busy timeout; only tests set it. |
| `SPARKWING_NAMESPACE` | Kubernetes namespace for `sparkwing debug attach`; only tests set it. |
| `SPARKWING_AUTO_REGISTER_WORKTREES` | `1` lets automatic repo registration include git worktrees, which it otherwise skips; nothing sets it. |
| `SPARKWING_DOCS_BASE_URL` | Base URL that `sparkwing docs` links point at; tests set it, and an earlier changelog entry offers it. |
| `SPARKS_GO_BIN` | The `go` binary the sparks resolver runs when it writes a pipeline's module overlay; nothing sets it. |
