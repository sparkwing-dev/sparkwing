# W7 storage handoff

W7 remains incomplete. Session 4 removes the remaining gitcache and package
proxy options, their caller plumbing, and cache-service discovery. The earlier
blob, artifact and memo implementations remain in place. No other area's
contract was changed.

## Implemented in session 4

- Removed runner `--gitcache`, `--dependency-proxy` and
  `--trigger-artifact-store`; launcher `--cache`, `--gitcache` and
  `--dependency-proxy`; agent `gitcache`; and the corresponding configuration
  fields. Direct Git is the only source mode. Repository allowlists still gate
  machine-owner credential fallback and working-tree admission.
- Removed the service installer's gitcache environment input and YAML output.
  The installer test still exercises YAML-injection refusal through the
  controller URL, and verifies that obsolete cache inputs do not enter config.
- Removed automatic package-registry environment injection from Kubernetes
  Jobs and host base environments. Plain registry settings work through
  `run.submit_env_allow`. Network proxy and CA settings remain available and
  their credentials remain masked. The existing operator filter still refuses
  embedded credentials, as required by the frozen environment policy.
- Removed `WithGitcache`, its unused URL argument, and the unused pod grant
  field. The old remote binary/source path now obtains its existing run grant
  where it uses it, under the node's own token. It no longer receives a cache
  grant from the parent's environment. **This does not finish the grant cut:**
  `directdata`, grant minting/renewal and old controller handlers still remain.
- Removed controller `--cache-url`, `--cache-pod-url`, `WithCacheURL`,
  `WithCachePodURL`, the `cache_pod` discovery field, the CLI gitcache health
  probe, and the unused `TeamStorage`/`WithTeamStorage` surface. Removed stale
  gitcache route metadata and metering helpers. Team purge already stopped
  calling the cache service in an earlier session; this removes its dead
  configuration and fixtures, without adding a replacement purge path.
- Replaced the retired cache-service guide with a storage contract page that
  states which integration is pending. Updated installer, environment, source,
  security and cache documentation. Regenerated CLI pages in process through a
  temporary Go test overlay, controller API docs/OpenAPI and the controller Go
  API snapshot, then synchronized embedded docs.

## Deleted

Four complete files, **1,301 lines**, are removed:

- `cmd/sparkwing-controller/cache_grant_key_test.go`: 39 lines.
- `pkg/controller/stream_deadline_test.go`: 120 lines.
- `docs/gitcache.md`: 571 lines.
- `pkg/docs/mirror/gitcache.md`: 571 generated lines.

Retained files also lose retired fields, flag parsers, environment builders,
cache-service health/deletion/proxy tests, and obsolete documentation. The
removed test behavior depended on a service or route that no longer exists.
Team row deletion, financial refusals and the log recheck tests remain.

## Acceptance and test ports

**Native storage acceptance: 0 activated; 424 target cells remain skipped.**
`acceptance.New`/`Run` still panic, as do the tenant record operations and
capability data authorization in this branch. Catalog, documentation and port
inventory checks pass. A skipped native cell is not a storage proof.

No additional T1 storage port was closed in this session. Earlier ports in
`internal/blob` and `internal/artifacts` still pass, including verified transfer,
receipt identity, edge refusal, memo materialization and artifact integrity.

New or migrated behavior checks cover:

- Rejection of removed agent and launcher configuration. Both new refusal
  tests failed before the implementation cut and pass afterward.
- Preservation of repository allowlists and refusal before source fetch.
- Pod environments omit cache-service settings and inherited cache grants.
  Explicit plain registry settings pass through the operator allowlist.
- Network proxy/CA forwarding and credential masking remain covered. The old
  package-proxy host fixtures now use network proxy variables.
- Installer output excludes gitcache and cache tokens. Its injection case now
  targets the surviving controller URL input.
- Deletion fixtures inspect retained/purged team rows instead of calls to the
  deleted cache service. These tests compiled; their native store/controller
  integration was not claimed green.

## Verification

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOCACHE`, `GOTMPDIR`
and `TMPDIR` beneath `/tmp/sw-astra-W7`, and `-p 2`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- Vet on `internal/blob/...`, `internal/artifacts`, `internal/agentconfig`,
  `internal/cluster`, `internal/runners/k8s`, `internal/runners/launcher`,
  `internal/orchestrator`, `internal/discovery`, `pkg/controller`,
  `cmd/sparkwing` and `cmd/sparkwing-controller`.
- Full `internal/blob/...` and `internal/artifacts` tests.
- Focused configuration, launcher flag, agent allowlist, pod environment,
  operator registry, proxy masking and pre-fetch source refusal tests.
- Runner CLI service-account validation after removal of the cache flag.
- Changed controller, CLI and discovery test packages compile.
- Installer test with stub service-manager commands; ShellCheck on both
  changed shell scripts.
- API reference/OpenAPI tests, generated CLI registry checks, embedded docs
  drift checks and acceptance catalog/port inventory checks.
- `git diff --check`.

The broader `internal/engineenv` suite failed on `SPARKWING_JOB_ID` and
`SPARKWING_API_TOKEN`: the old classifier does not classify the names already
in `internal/procenv/registry.go`. Both entries exist at HEAD and neither
package changed here. This needs the W6 environment cut; it is not a passing
check.

A fresh Astra reviewer inspected an immutable snapshot. It found no concrete
code regression and identified premature integration claims in two docs. Both
claims now state that remote dependency-cache host wiring is pending. The
review also removed a stale grant comment and empty fixture callbacks.

## Contracts and real integration still open

No new mechanism or local contract change was added in this session. Existing
contract gaps and integration obligations remain:

- **W1:** implement tenant `RecordStore`, read sets, atomic memo copy, live blob
  existence, unique-byte quota, receipts, retention and tombstones. Declare
  tenant upload-ticket/reservation/receipt lifecycle methods before the
  controller can bind a verified object commit atomically. Team deletion also
  needs final CAS/log storage cleanup through authoritative records.
- **W2/W7:** replace old `directdata` handlers and clients with capability
  transport. Reauthorize upload tickets at commit, reserve quota before
  signing, commit exact receipts and mount/configure the filesystem or S3
  signer. Remove cache grants, their environment/renewal paths, service-token
  accounting routes and old storage clients. Logs/publication need their
  final runtime bindings too.
- **W3/W8:** provide per-attempt blob/record bindings to artifact and memo
  consumers, including after retries. Bind `depcache.WithBackend` in real
  hosts; production dependency caches still default to local files. Complete
  store-owned memoized lifecycle, generator replay and retry materialization.
- **W4:** finish `.Cache` and migrate the repository lint declaration.
- **W6:** finish environment ownership and replace the old classifier.
- **T1/lead:** implement the native driver and execute the full storage matrix,
  including SQLite/Postgres, filesystem/S3, Windows and all placements.

Continue with the W7 capability byte/record clients, binary/source migration,
remaining legacy storage deletion and native acceptance. The existing blob
interfaces and fakes remain the boundary while other areas are stubs. No
product decision or permission question is pending.

## Friction and limits

- The sandbox cannot run socket-backed pipelines. No real controller, S3,
  Postgres, Kubernetes or native Windows run was attempted. The lead owns
  pipeline/gate execution. Full controller/orchestrator suites are not claimed.
- The first API regeneration refused the removed `cache_pod` schema member.
  Removing that member and retired cache routes made regeneration and the
  OpenAPI drift tests pass.
- `sparkwing info --for-agent` ran once during bootstrap, before loading the
  swarm's no-binary instruction. No candidate, daemon or pipeline was run.
- No git writes. The repository identity remains Korey Gambill and
  `core.worktree` is unset. Temporary caches and the review snapshot under
  `/tmp/sw-astra-W7` are removed at handoff.

## Shared files touched in session 4

This list includes generated files and the removed API callers.

- `CHANGELOG.md`.
- `.apidiff/pkg_controller.txt`.
- `api/openapi.yaml`.
- `bin/service-install-test.sh`.
- `cmd/sparkwing/`: `cloud.go`, `health.go`, `help_registry.go`, `profiles_test_cmd.go`, `profiles_test_cmd_test.go`, `runners_test.go`.
- `cmd/sparkwing-controller/`: `cache_grant_key_test.go`, `main.go`.
- `docs/`: `_sidebar.json`, `api-reference.md`, `architecture.md`, `auth.md`, `backends.md`, `backup-restore.md`, `caching.md`, `cli-cloud.md`, `cli-cluster.md`, `cli-pipeline.md`, `environment-variables.md`, `gitcache.md`, `limits.md`, `local-execution.md`, `node-protocol.md`, `observability.md`, `security.md`, `self-hosting.md`, `sparks-core.md`, `sparks.md`, `storage.md`.
- `install/service-install.sh`.
- `internal/agentconfig/`: `agentconfig.go`, `agentconfig_test.go`.
- `internal/cluster/`: `launch_cli.go`, `pipeline_source_test.go`, `pool_setup_failure_test.go`, `runner_agent_cli.go`, `runner_agent_cli_test.go`, `runner_identity_test.go`, `runner_pool_cli.go`, `runner_pool_cli_test.go`, `storage_flags_test.go`, `trigger_binary_auth_test.go`, `trigger_loop.go`, `trigger_loop_test.go`.
- `internal/discovery/`: `discovery.go`, `discovery_test.go`.
- `internal/orchestrator/`: `k8s_runner_factory.go`, `pod_env_test.go`, `pod_node_host.go`, `repo_admission_test.go`, `run_node.go`, `run_node_claim_test.go`, `run_node_options.go`, `run_node_remote.go`, `run_node_remote_cancel_test.go`, `trigger_host.go`, `trigger_host_test.go`, `wingd_api.go`.
- `internal/runners/k8s/`: `k8s.go`, `k8s_test.go`.
- `internal/runners/launcher/`: `job.go`, `job_test.go`.
- `pkg/controller/`: `cache_grant_internal_test.go`, `deletion.go`, `deletion_review_test.go`, `deletion_test.go`, `egress.go`, `egress_internal_test.go`, `egress_test.go`, `handlers.go`, `identity_team.go`, `runner_claim.go`, `server.go`, `services.go`, `services_auth_test.go`, `storage_counters_test.go`, `stream_deadline_test.go`, `tenant_routes.go`.
- `pkg/docs/changelog.md`.
- `pkg/docs/mirror/`: `_sidebar.json`, `api-reference.md`, `architecture.md`, `auth.md`, `backends.md`, `backup-restore.md`, `caching.md`, `cli-cloud.md`, `cli-cluster.md`, `cli-pipeline.md`, `environment-variables.md`, `gitcache.md`, `limits.md`, `local-execution.md`, `node-protocol.md`, `observability.md`, `security.md`, `self-hosting.md`, `sparks-core.md`, `sparks.md`, `storage.md`.

STATUS: CONTINUE
