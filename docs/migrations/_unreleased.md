# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

## (Breaking) Runner scopes split out of admin

- **Before:** The routes a runner calls to do its job all required `admin`:
  `POST /api/v1/triggers/claim`, `/triggers/{id}/heartbeat`, `/triggers/{id}/done`,
  `POST /api/v1/runs`, `/runs/{id}/finish`, `/runs/{id}/nodes`,
  `/runs/{id}/nodes/{nodeID}/start`, `finish`, `/runs/{id}/events`, and
  `GET /api/v1/secrets/{name}`. `docs/auth.md` and both chart READMEs said a
  runner needed `nodes.claim` plus `logs.write`, so an operator who followed
  them shipped a broken runner and fixed it by granting `admin`. The token in
  the pod that executes pipeline code could therefore mint tokens, read every
  user, and read every secret in the cluster.
- **After:** Three new scopes carry that work. `triggers.claim` unlocks the
  trigger worker lifecycle. `runs.state` unlocks run create and finish, plan
  snapshot, node create, event append, and the per-node `start`, `finish`,
  `deps`, and `status` writes. `secrets.read` unlocks
  `GET /api/v1/secrets/{name}` alone. Every `runs.state` write is bound to a
  run the caller owns: it holds an unexpired claim on one of the run's nodes,
  or the unexpired claim on the run's trigger, which schema 23 records against
  the claiming token. A run's repository comes from that trigger and is written
  once, so a runner cannot repoint its run and read another repository's
  credential.

  Secrets gained an owning repository and a shared flag: run-store schema 22
  widens the secrets primary key to `(name, repo)`, schema 23 adds
  `secrets.shared` defaulting to `0`, `sparkwing secrets set --repo <slug>`
  stores a repository-scoped row, and `--shared` stores an unscoped row every
  run may read. A `secrets.read` principal without `admin` names the run it is
  executing with `?run=<id>`; the controller answers only when the caller holds
  that run's claim, and resolves the name against that run's repository,
  falling back to an unscoped row only when that row is shared. `admin` remains
  a superset, so existing tokens keep working, and an `admin` read may pass
  `?repo=` or `?run=` to reach a repository's own row. Neither the trigger loop
  nor `sparkwing worker` puts `--token` on the child process argv; the child
  reads `SPARKWING_AGENT_TOKEN` from its environment.
- **Migration:** Upgrade the controller before the runners so the schema-23
  tables exist; older binaries refuse the upgraded SQL store. Re-mint each
  runner token with `nodes.claim`, `triggers.claim`, `runs.state`,
  `secrets.read`, and `logs.write`, and drop `admin` from it. That set drives a
  whole pipeline, in-process or across a node pool. A warm-pool dispatcher that
  marks nodes ready keeps `admin`.

  Every secret that existed before this upgrade migrates unscoped and
  unshared, which means **no run can read it** until you act on it. Give each
  one either a repository with
  `sparkwing secrets set --name X --file ./x --repo <slug> --profile <p>` or,
  for a value every repository legitimately shares, `--shared` in place of
  `--repo`. `sparkwing secrets list` prints each row's repository, or
  `(admin only)` for one that is neither. Do this before pointing runners at
  the upgraded controller, or their first secret read answers `404`.

  Go callers of `store.CreateOrReplaceSecret` pass a `store.Secret` value and
  the timestamp, `store.DeleteSecret` takes the repo slug as a second argument,
  `store.RepoForPrincipalClaim` is replaced by `RepoForClaimedRun` and
  `ReposForClaimant`, and `store.ClaimNextTriggerFor` takes a
  `store.ClaimIdentity` after the context. `client.CreateSecretForRepo` takes a
  trailing `shared bool`; `client.GetSecretForRun` is the read a runner makes.
- **Why:** Every pool replica and laptop agent holds a runner token, and the
  process that executes pipeline code holds it too. A token scoped to run work
  should not be able to mint an admin bearer, finish a stranger's run, or read
  another repository's deploy key with one `os.Getenv`. Keeping the bearer out
  of the pipeline body's reach entirely, by brokering these calls through a
  supervisor process, is the remaining design step.

## Session rows are hashed and the CSRF column is dropped

- **Before:** `sessions` held the raw browser session id and its CSRF token in
  plain columns. Every migration up to schema 20 was additive, so a replica on
  an older binary kept working against a newer database.
- **After:** Schema 21 deletes every row in `sessions`, drops the
  `sessions.csrf_token` column, and keys rows by `sha256(session id)`. The CSRF
  token is derived per request as an HMAC of the session id under a key in
  `sparkwing_meta`. This is the first destructive schema step: an older binary
  still running against the migrated database selects `csrf_token`, fails, and
  answers `401` to every dashboard request.
- **Migration:** Stop every controller sharing the state database, upgrade them
  all, then start them. Do not roll the upgrade one replica at a time, and do
  not roll a replica back past schema 21 once it has run. Everyone signed in to
  the dashboard signs in again; there is no way to carry sessions across the
  migration, because the pre-21 rows are exactly the replayable ids the change
  removes. On PostgreSQL the column drop takes an ACCESS EXCLUSIVE lock on
  `sessions` inside the migration transaction, so run it when the table is idle.
- **Why:** A copy of the state database, its WAL, or a backup handed the reader
  a working dashboard session. Storing only the digest, and deriving the CSRF
  token, means a database reader holds nothing it can replay.

## The dashboard refuses an unauthenticated remote bind

- **Before:** `sparkwing-web --token ... --addr=0.0.0.0:4343` without
  `--require-login` served an open dashboard and injected the controller bearer
  into every HTML page, so any browser that reached the listener held the
  token.
- **After:** The bearer stays in the web process and rides only its server-side
  proxy. That configuration now fails at startup with a message naming the
  three ways forward.
- **Migration:** Turn on `--require-login` (chart: `web.requireLogin`), bind a
  loopback address (chart: `web.addr`, which defaults to `0.0.0.0:<port>` and
  reaches the Service only from that default), or keep the open dashboard by
  passing `--allow-unauthenticated-remote` (chart:
  `web.allowUnauthenticatedRemote`). Drop `--api-url`: the dashboard proxies
  the API on its own origin, which is also what the new `connect-src 'self'`
  policy allows. The chart no longer renders the flag and `web.apiUrl` is gone
  from `values.yaml`; leaving it set in your own values file does nothing.
- **Why:** An unauthenticated dashboard holding a service token hands the
  controller to every caller that can reach the port.

## Cache reads require the bearer token

- **Before:** `sparkwing-cache` demanded a bearer only on its blob and sync
  routes. Git clone and registration, `/archive`, `/file`, `/tree-hash`,
  `/branch-contains`, `/repos`, and `/artifacts/` answered anyone who could
  reach the port.
- **After:** Every route that touches repository content answers 401 without a
  valid bearer. `/health`, `/metrics`, `/stats`, and the package proxy under
  `/proxy/` stay open. `POST /git/register` also validates `name` against
  `^[A-Za-z0-9._-]{1,64}$` and refuses to repoint an existing name without the
  token.
- **Migration:** Give every client that reads the cache directly the same token
  the cache runs with. The charts do this: the runner and the controller read
  `SPARKWING_CACHE_TOKEN` from `controller.tokenSecret`. Hand-rolled callers add
  `Authorization: Bearer <token>`. A cache deliberately left open keeps
  `--allow-unauthenticated` (`SPARKWING_CACHE_ALLOW_UNAUTHENTICATED=1`), which
  logs a warning at startup.
- **Why:** The cache holds the deploy key, mirrored private source, and
  uncommitted working-tree snapshots. Reaching its Service proves nothing about
  the caller.

## Cache Service and NetworkPolicy defaults

- **Before:** No chart shipped a NetworkPolicy, and `cache.service.type` was a
  free knob.
- **After:** `sparkwing-runner-bundle` renders a default-deny ingress
  NetworkPolicy for the cache pod admitting the release's runner, controller,
  and dashboard pods plus the Job pods the Kubernetes runner backend creates
  (`app.kubernetes.io/name: sparkwing-runner`), and fails the render when
  `cache.service.type` is not `ClusterIP` while no token Secret is configured.
- **Migration:** A cluster whose CNI enforces NetworkPolicy and whose
  controller or runner pool lives outside the cluster adds peers through
  `networkPolicy.extraIngress`, giving the rule an `ipBlock` for the caller's
  source range. A dashboard or controller under a different release points
  `networkPolicy.webPodSelector` or `networkPolicy.controllerPodSelector` at
  its own pod labels, and `networkPolicy.runnerJobPodSelector` matches the
  runner Jobs. `networkPolicy.enabled=false` removes the policy. A published
  cache Service needs `controller.tokenSecret.name` set and
  `cache.allowUnauthenticated` left false.

## The controller is told where its cache is

- **Before:** `sparkwing-full` gave the controller `SPARKWING_CACHE_TOKEN` but
  no cache URL, so every `/api/v1/gitcache/*` route answered
  `404 gitcache proxy is not configured` and `pipeline trigger --working-tree`
  from off-cluster failed at the seed.
- **After:** The controller Deployment also carries `SPARKWING_CACHE_URL`,
  pointing at the bundled cache Service whenever the sub-chart and its cache
  are enabled.
- **Migration:** None for a stock install. A cache you run yourself goes in
  `controller.cache.url`.

## Cache metrics no longer name repositories

- **Before:** The unauthenticated `/metrics` endpoint exported one fetch and
  reclone series per mirrored repository, labelled with the repository
  directory name, which is an offline-computable hash of the clone URL.
- **After:** Those series carry no `repo` label, so scraping `/metrics` cannot
  enumerate the mirror set or confirm a guessed repository.
- **Migration:** A dashboard that broke fetch duration out per repository loses
  that split. Aggregate views are unchanged.

## Expired workspace seeds are archived, not deleted

- **Before:** A workspace ref older than `WORKSPACE_SEED_MAX_AGE` was deleted
  on the next seed and its objects were pruned immediately, so retrying an
  older `pipeline trigger --working-tree` run failed with a missing object.
- **After:** Expiry moves the ref to `refs/sparkwing-workspace-archive/`, which
  keeps the objects reachable for another seven times
  `WORKSPACE_SEED_MAX_AGE` or until 128 archived refs accumulate.
- **Migration:** None. Raise `WORKSPACE_SEED_MAX_AGE` if your retries run
  further behind than the archive window; set it negative to disable expiry.

## Managed Git hooks run locally by default

- **Before:** A managed hook installed without `--profile` inherited the
  pipeline or project default profile. Its proof and later Git actions could
  fail when that profile's controller was offline.
- **After:** Installing or reinstalling without `--profile` proves and renders
  the hook with `--sw-local-only`. An explicit `--profile NAME` still pins the
  hook to shared storage. Local-only coordinated nodes no longer reopen remote
  cache, logs, or secrets.
- **Migration:** Run `sparkwing pipeline hooks install` again to adopt the
  local default. If a hook needs shared storage, reinstall it with
  `sparkwing pipeline hooks install --profile NAME`. Existing hook files do not
  change until reinstalled.
- **Why:** A local Git action should not require an intermittently available
  controller unless the repository owner opts into that dependency.

## Pipeline name charset

- **Before:** `sparkwing.yaml` accepted any string as a pipeline `name`,
  including `e2e/k8s`, `my pipeline`, and names holding a quote or a shell
  metacharacter.
- **After:** A name must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`: it starts with an
  ASCII letter or digit and then holds only letters, digits, `.`, `_`, and `-`.
  A config with any other name fails to load, so `sparkwing run`, `pipeline
  list`, `pipeline hooks install`, tab completion, the dashboard, and the
  orchestrator all refuse it with `pipeline "<name>": name must match
  ^[A-Za-z0-9][A-Za-z0-9._-]*$`.
- **Migration:** Rename each offending pipeline. The YAML `name` and the string
  passed to the SDK's `Register` call must stay equal, so both change together.

  ```yaml
  # before
  pipelines:
    - name: e2e/k8s
      entrypoint: Gate

  # after
  pipelines:
    - name: e2e-k8s
      entrypoint: Gate
  ```

  ```go
  // before
  sw.Register[sw.NoInputs]("e2e/k8s", func() sw.Pipeline[sw.NoInputs] { return &Gate{} })

  // after
  sw.Register[sw.NoInputs]("e2e-k8s", func() sw.Pipeline[sw.NoInputs] { return &Gate{} })
  ```

  Then update every caller of the old name: `sparkwing run <name>` in CI jobs,
  scripts, and schedules. Re-run `sparkwing pipeline hooks install` so the
  generated git hooks invoke the renamed pipeline.
- **Why:** The name reaches generated git hook scripts, argv, log lines, and
  file paths. A cloned repository could otherwise hand shell execution to
  anyone who ran the documented hooks install command.

## Node claims bind to the claiming token

- **Before:** Any `nodes.claim` token could write any node of any run, stamp
  `ready_at` on a node whose dependencies had not finished, and read any run's
  plaintext secret arguments through `?include=secret_values`. Scope gated the
  route; nothing gated the object.
- **After:** `POST /api/v1/nodes/claim` records the claiming token's prefix
  segment alongside the principal name and `holder_id`, and every gated route
  matches on the prefix, so two tokens sharing a principal name cannot act on
  each other's claims. The per-node write routes (`activity`, `touch`,
  `annotations`, `summary`, `artifact-manifest`, `metrics`, `dispatch`,
  `steps/*`, `bounce/consume`) answer `403` with `"error": "claim_required"`
  unless the caller holds that node's unexpired claim, and `heartbeat` answers
  `409` unless the token, the principal, and the holder id all match.
  `mark-ready` and `revoke-ready` require `admin`. `lease_secs` is clamped to
  10 minutes on the claim and on every heartbeat. The node read routes
  (`nodes/{id}`, `nodes/{id}/output`, `nodes/{id}/bounce`) and
  `POST /runs/{id}/heartbeat` answer `403` unless the caller holds a claim on
  some node of that run, carries `runs.read`, or is `admin`.
  `PUT /api/v1/pipelines/{name}/profile/pin` requires `runs.state`. The execution
  view returns plaintext arguments to an `admin` principal, or to a
  `nodes.claim` principal holding an unexpired claim on one of the run's nodes;
  a controller serving unauthenticated returns plaintext, because the whole API
  is open in that mode and a redacted argument would execute as the literal
  `***`.
- **Migration:** Give the token that dispatches nodes to a warm pool `admin`,
  which it already needs to create, start, finish, and mark nodes ready; it now
  also needs `admin` to call `revoke-ready`. A dispatcher that sizes pods needs
  `runs.state` to write a pipeline resource pin. Pool runners that claim their own work keep `nodes.claim`, and need
  `runs.read` as well if their pipelines resolve cross-pipeline references.
  Claims taken before the upgrade carry no token prefix, so a runner in flight
  during the upgrade loses its lease and the node is requeued.
- **Why:** Every laptop agent and pool replica holds a runner token, and the
  documented Helm deployment gives every replica the same one. A token scoped
  to claim work should not read another repository's deploy credentials, force
  a node to run before its dependencies finish, or pick how long its own
  authorization lasts.

## Dashboard proxy allow-list

- **Before:** `sparkwing-web` forwarded any `/api/v1/` path to the controller
  with its service bearer attached, and every browser session was minted with
  the `admin` scope. One dashboard login reached every admin route.
- **After:** The proxy forwards only the routes the dashboard calls and
  answers `404` for the rest, and it forwards reads only to the logs service.
  A session carries the scopes of the user who signed in, and the proxy checks
  them against the scope the controller registers for the target route.
  Run-store schema 19 adds `users.scopes`, defaulting every existing account to
  `admin`.
- **Migration:** Upgrade the controller before the web pod so the schema-19
  column exists when the first login resolves scopes. Re-mint the web pod's
  controller token with `runs.read` and `logs.read`, adding `runs.write` where
  operators cancel, retry, or release runs from the dashboard,
  `approvals.write` where they resolve approval gates, and `admin` where they
  delete runs from the dashboard, which the controller registers at `admin`.
  Create narrower dashboard accounts with
  `sparkwing cluster users add --scope`; existing
  accounts keep `admin` until an operator replaces them. Callers of
  `store.CreateUser` and `store.CreateFirstUser` pass the account's scopes as
  a new `[]string` argument before `now`.
- **Why:** A dashboard login was an admin bearer, so any account that could
  sign in could read every secret and mint tokens.

## Secret input hash migration

- **Before:** Run-store schema 17 persisted a deterministic `inputs_hash` even
  when the caller supplied an argument declared `secret:"true"`. A reader could
  verify guesses of a low-entropy value without seeing the redacted argument.
- **After:** Schema 18 removes legacy hashes from SQL rows that supplied a
  classified secret argument. New runs omit the invocation hash, receipts leave
  `identity.inputs_hash` empty, and read-time redaction suppresses hashes from
  legacy state objects. Built-in SQL and S3 state backends reject an unsafe
  `CreateRun`; the controller maps that rejection to HTTP 400 before the run can
  emit its start record.
- **Migration:** Stop the controller and every runner or local process that can
  write run state or logs. Upgrade the whole fleet before opening schema 18 or
  writing another S3 state object, then resume it together. Schema-17 binaries
  refuse the upgraded SQL store; object storage has no equivalent schema gate.
  A custom `storage.StateStore` should call `store.ValidateRunInvocation` from
  `CreateRun` and return `store.ErrSecretInputHash` unchanged.
- **Why:** A deterministic digest is not a safe commitment to a secret value.

## Dispatch snapshot credentials

- **Before:** A node dispatch snapshot captured every `SPARKWING_` and `GITHUB_`
  environment variable, including the runner's controller bearer, stored the
  values unmasked, and served them to any `runs.read` token.
- **After:** Capture drops any key whose name reads as a credential
  (`TOKEN`, `SECRET`, `PASSWORD`, `KEY`, `CREDENTIAL`, and similar), masks
  registered secret values in the rest, and records the dropped names in
  `redacted_keys`. Schema 20 adds that column. The dispatch read routes return
  `env_json` only to an `admin` principal; every other reader still gets the
  key list. Cluster-mode `sparkwing debug rerun` creates its debug pod from a
  manifest on `kubectl` stdin rather than `--env=K=V` arguments.
- **Migration:** Upgrade the fleet before opening schema 20; older binaries
  refuse the upgraded SQL store. Give any tooling that reads `env_json` an
  `admin` token, or have it read the run's own environment instead. Export a
  credential a rerun needs into the debug shell yourself; the banner names the
  keys the snapshot dropped.
- **Why:** A read-only token could otherwise lift the runner's admin bearer out
  of a snapshot.

## Kubernetes acceptance testing

- **Before:** `sparkwing run kind-e2e` built images locally, created a Kind
  cluster, and also had an existing-cluster mode selected by
  `SPARKWING_KIND_E2E_PROVISION=existing`. A path-scoped hosted workflow ran the
  Kind mode automatically.
- **After:** `sparkwing run k8s-e2e` targets only an explicit Kubernetes
  context. Set `SPARKWING_K8S_E2E_KUBE_CONTEXT`,
  `SPARKWING_K8S_E2E_IMAGE_PREFIX`, `SPARKWING_K8S_E2E_TAG`, and
  `SPARKWING_K8S_E2E_ALLOW_CLEANUP`. The check creates only a uniquely owned
  namespace and release resources. It leaves cluster infrastructure intact.
- **Migration:** Rename `SPARKWING_KIND_E2E_*` variables to
  `SPARKWING_K8S_E2E_*`, remove `SPARKWING_KIND_E2E_PROVISION`, and invoke
  `sparkwing run k8s-e2e` only when the designated test cluster is active.
- **Why:** Acceptance evidence should come from the Kubernetes environment the
  product will use, without requiring a memory-heavy local cluster or spending
  cluster capacity on every source change.

## Runner ServiceAccount tokens and RBAC

- **Before:** The runner-bundle Role granted `get`, `list`, and `watch` on the
  namespace's Secrets, ConfigMaps, pods, and events. Every pod in both charts
  automounted its ServiceAccount token, so pipeline code could read the
  controller bearer, the webhook HMAC, and the secrets-at-rest key straight from
  the API. `--runner k8s` with no `--runner-sa` landed pipeline pods on the
  namespace default account. The warm-pool warmer pod named no account at all.
- **After:** The Role carries `rules: []`. No pod mounts a token unless
  `runner.automountServiceAccountToken=true` asks for one. The runner, cache,
  and logs pods each have their own ServiceAccount, and `sparkwing-full` creates
  a release-scoped `<release>-sparkwing-full-cache-warmer` account that the
  controller names through `--warmer-service-account` (env
  `SPARKWING_WARMER_SA`).
- **Migration:** A pipeline that called the Kubernetes API from a runner pod -
  `kubectl get configmap`, a sealed-secrets read, an in-cluster helm install -
  stops working: give it its own ServiceAccount and Role outside these charts.
  A runner running the opt-in Kubernetes trigger runner
  (`SPARKWING_TRIGGER_RUNNER=k8s`) needs its token back with
  `--set runner.automountServiceAccountToken=true`, plus a Role that grants
  `batch/jobs` create; that runner now refuses to start without `--runner-sa`.
  An install that sets `serviceAccount.create=false` must add
  `serviceAccount.shareAcrossComponents=true` to accept one account for all
  three pods. A controller running the warm pool outside `sparkwing-full` must
  create the warmer ServiceAccount in the pool namespace
  (`kubectl create serviceaccount sparkwing-cache-warmer -n <pool-namespace>`)
  or point `--warmer-service-account` at an existing one. Go integrations must
  pass that name to `pool.WarmPVC` and `pool.WarmingLoop`, or set
  `controller.PoolConfig.WarmerServiceAccount`. On EKS or GKE, a
  trust policy scoped to the old shared account must name the new `-cache` and
  `-logs` accounts before the upgrade.
- **Why:** Pipeline authors are expected to run code on runners. They are not
  expected to read the key that decrypts every stored secret or the HMAC that
  authenticates every webhook.
