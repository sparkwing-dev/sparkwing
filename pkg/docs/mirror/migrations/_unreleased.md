# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

## (Breaking) Submitted runs carry an allow-listed environment

- **Before:** `sparkwing runs submit` snapshotted the whole submitting shell to
  disk and handed it to the run, so the queued snapshot held whatever the
  terminal held: `AWS_SECRET_ACCESS_KEY`, `OPENAI_API_KEY`, a `kubectl` bearer.
  A run that a consumer shutdown returned to the queue dispatched again from the
  consumer's own environment.
- **After:** Capture keeps only `SPARKWING_*`, `GITHUB_*`, `PATH`, `HOME`,
  `HOSTNAME`, and `KUBERNETES_SERVICE_HOST`, then drops every credential-shaped
  name and value from that set. `SPARKWING_SUBMIT_ENV_ALLOW` widens it by name,
  or by prefix with a trailing `*`; a bare `*` is refused at submission time,
  and the credential filter logs at warn the names it removes from an entry an
  operator wrote by hand. The consumer deletes the snapshot when it starts the
  run, and a run that returns to the queue without its snapshot fails with
  "submission environment snapshot is gone" rather than running under the
  consumer's shell.
- **Migration:** A submitted pipeline that read `AWS_PROFILE`, `AWS_REGION`,
  `KUBECONFIG`, `DOCKER_HOST`, or `SSH_AUTH_SOCK` from the submitting shell
  stops seeing them. Most of those fail loudly; `AWS_PROFILE` and `DOCKER_HOST`
  do not, because the AWS SDK and the Docker client fall back to a default
  profile and socket, which can point a deploy at the wrong account. Name what
  each pipeline needs:

  ```bash
  SPARKWING_SUBMIT_ENV_ALLOW='AWS_PROFILE,AWS_REGION,KUBECONFIG,DOCKER_HOST,SSH_AUTH_SOCK' \
    sparkwing runs submit deploy
  ```

  The credential filter still applies to what the list names, so a value that
  reads as a secret is dropped even when named; the warn line says which. Take
  credentials from the secret store instead. Go callers of
  `orchestrator.CaptureSubmissionEnvironment` pass a `*slog.Logger` as a
  trailing argument.

  Resubmit any run that was queued when a consumer was interrupted: its
  snapshot is gone and the requeued dispatch now fails instead of running with
  the consumer's environment.
- **Why:** A queued run is a file on disk that outlives the shell that made it.
  It should not be a copy of every credential that shell happened to export,
  and losing the snapshot should narrow what a run can reach, not widen it.

## (Breaking) Clone hosts in inward-only name spaces are refused

- **Before:** A clone URL was checked against the loopback, private,
  link-local, carrier-grade-NAT, and metadata rules only when its host parsed
  as an IP address. Any name passed: a forge under `.internal`, a `.local` box on the
  LAN, `ip6-localhost`. An scp-like URL carrying a second `@`
  (`git@a@127.0.0.1:repo.git`) was checked as a host named `a@127.0.0.1`,
  which parses as no address at all, while ssh split the destination at the
  last `@` and dialled the loopback address.
- **After:** A host that is, or ends in, `internal`, `local`, `localdomain`,
  or `home.arpa` is refused, as are the `ip6-*` aliases from the standard
  `/etc/hosts`. An scp-like host must read as a hostname, so a second `@` is
  refused outright. `POST /api/v1/triggers` answers 400 for these, as do the
  `sparkwing-cache` routes `/git/register`, `/archive`, and `/sync/seed`.
- **Migration:** A deployment that clones from an internal forge under one of
  those suffixes -- a forge named under `.internal` or `.local` -- stops being able to
  submit triggers or register repositories for it. Give the forge a name
  outside those name spaces, which is what a name resolvable off the LAN
  already needs.

  `sparkwing-cache` revalidates `repo-names.json` when it starts, so an entry
  registered before this release under such a name is dropped with a
  `warning: dropping repo "<name>" from repo-names.json` line and its cached
  clone stops being served. Check the log after upgrading:

  ```bash
  kubectl logs deploy/sparkwing-cache | grep 'dropping repo'
  ```

  Re-register anything listed there under a name the validator accepts. The
  check is deliberately a name check and not an address check; see
  [docs/security.md](../security.md) for what it does not cover and why
  egress policy is the control that does.

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

## The dashboard refuses an insecure-cookie remote bind

- **Before:** `SPARKWING_WEB_INSECURE_COOKIES=1` dropped `Secure` from the
  session and CSRF cookies on any bind address, so a dashboard published over
  plain HTTP handed both cookies to every network between the browser and the
  pod.
- **After:** `sparkwing-web` reads the variable once at startup and exits
  before listening when the bind address is not loopback. A deployment that
  carries the variable on a `0.0.0.0` bind crashloops after the upgrade, with
  the refusal in the pod log.
- **Migration:** Serve the dashboard over HTTPS and drop the variable, bind a
  loopback address (chart: `web.addr`) for a port-forward or sidecar, or keep
  the plain-HTTP publication by adding `--allow-insecure-cookies-remote`. The
  chart renders both the flag and the variable whenever
  `ingress.allowInsecure` is on, so a chart-managed deployment needs no change.
- **Why:** A cookie without `Secure` travels in clear text, and the bind
  address is the only evidence the process has that nobody else is listening.

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
  `batch/jobs` create; that runner now refuses to start without
  `--trigger-runner-sa` (or `SPARKWING_RUNNER_SA`).
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

## Logs service quotas and bounded search

- **Before:** `sparkwing-logs` stored whatever runners posted. Nothing capped a
  node's log, a run's total, or the volume, and a full disk surfaced as a
  degraded health probe after the writes had already failed.
  `GET /api/v1/logs/search` accepted a query with no `run_id`, walked every
  stored run end to end, and kept scanning after the caller hung up.
- **After:** A node log stops at `--max-node-bytes` (64MiB) and a run's logs at
  `--max-run-bytes` (1GiB). The append that crosses either cap stores the bytes
  that fit, appends `[sparkwing-logs] truncated: byte cap reached` once, and
  still answers `204`; later appends answer `204` and store nothing, so a
  chatty node degrades its own log rather than failing its run. An append with
  less than `--min-free-bytes` (512MiB) free answers `507`, which the log sink
  retries and then reports as `logs_dropped`, as does an append that arrives
  while in-flight request bodies already hold `--max-inflight-bytes` (32MiB),
  which answers `503`. Search requires `run_id` and answers `400` without one,
  reads at most `--search-max-bytes` (256MiB) for at most `--search-timeout`
  (10s), stops when the caller disconnects, and sets `"truncated": true` on a
  response any of those stopped.
- **Migration:** None for a stock install. Raise `--max-node-bytes` or
  `--max-run-bytes` if a pipeline legitimately emits more than the defaults and
  you would rather spend the disk than read a truncated log; the marker in the
  stored log tells you which runs hit a cap. Retention is off by default, so
  nothing deletes existing history until you opt in with `--retention`
  (`SPARKWING_LOGS_RETENTION`, for example `168h`), which sweeps every run whose
  last write is older than that window on the `--sweep-interval` tick. A caller
  that searched the whole store now passes `run_id`, and one that needs
  complete results checks `truncated` and narrows the query. On Kubernetes the
  runner-bundle chart carries every one of these as `logs.limits.*`, so sizing
  them for your volume no longer means forking the chart. A malformed or
  negative value stops the service at startup instead of quietly restoring the
  default, so check any `SPARKWING_LOGS_*` you already set (`7d` and `64MiB`
  are not accepted; use `168h` and `67108864`).
- **Why:** Every runner holds a token that may append, and one chatty pipeline
  could fill the volume for every other run or pin the service on a whole-store
  scan.

## Restricted pod security and the published-dashboard guard

- **Before:** Neither chart set a seccomp profile, every container could
  write to its image layers, and `ingress.enabled=true` rendered whatever
  `ingress.tls` and `web.requireLogin` held.
- **After:** Every pod carries `seccompProfile: RuntimeDefault`, every
  container runs with a read-only root filesystem over a `/tmp` scratch
  `emptyDir`, and the Kubernetes runner Job does the same. `sparkwing-full`
  refuses to render when `ingress.enabled=true` with an empty `ingress.tls`
  or with `web.requireLogin=false`. `ingress.allowInsecure` must be a bool:
  a quoted string fails the render instead of reading as an opt-out. Opting
  in with an empty `ingress.tls` sets `SPARKWING_WEB_INSECURE_COOKIES=1` on
  the web Deployment, so the login gate still works over plain HTTP. The
  `ingress.tls` check is presence-only, so an entry without `secretName`
  leaves TLS to the ingress controller's default certificate.
- **Migration:** An install that publishes the dashboard sets `ingress.tls`
  and `web.requireLogin=true` before upgrading, or sets
  `ingress.allowInsecure=true` (a bool, not `"true"`) to keep publishing it
  unencrypted or open. A
  custom image whose process writes outside `/tmp` needs its own
  `emptyDir` mount for that path, or `securityContext.readOnlyRootFilesystem`
  overridden for that container.

## GitHub webhook bindings and replay protection

- **Before:** Any holder of `GITHUB_WEBHOOK_SECRET` could drive any pipeline
  against any repository, and a captured delivery could be re-sent without
  limit.
- **After:** `GITHUB_WEBHOOK_BINDINGS` binds each pipeline to the repositories
  allowed to drive it and gives a pipeline or a repository its own signing
  secret. The document is parsed strictly: an unknown field, a syntax error, or
  content after the closing brace fails startup, and the controller logs the
  resolved counts -- pipelines, bound repositories, pipelines refusing every
  repository, repository secrets -- so a document that parsed to nothing is
  visible. A `repos` list that is present but empty now refuses every
  repository; a pipeline the document does not name, or that names no `repos`
  key, is unchecked as before. A delivery whose `repository.full_name` is not
  an ASCII `owner/name` slug is refused. Refusals are shaped so the status code
  does not enumerate the tables: an unbound repository answers `404`, and once
  any pipeline or repository carries its own secret, a delivery resolving to no
  secret answers `401` rather than `503`.

  Run-store schema 25 adds `triggers.webhook_replay_key`, a digest of the
  pipeline and the request body -- exactly what the HMAC signs -- under a
  store-wide unique constraint, alongside the schema 24 constraint on
  `X-GitHub-Delivery`. Re-sending an accepted body answers `409` however its
  delivery header reads, and the `409` body carries the `run_id` the first
  delivery produced.
- **Migration:** Upgrade the controller before the runners so the schema-25
  column exists; older binaries refuse the upgraded SQL store. Review any
  `GITHUB_WEBHOOK_BINDINGS` document for a `"repos": []` entry, which used to
  allow every repository and now allows none, and for anything after the
  closing brace, which used to be discarded and now fails startup. A caller
  that keyed on the `403` for an unbound repository reads `404` now, and a
  client that retried a webhook by changing `X-GitHub-Delivery` gets `409` with
  the original run instead of a second run. `store.Trigger` gains
  `WebhookReplayKey`, which `store.CreateTrigger` writes and no read returns;
  `store.FindTriggerByWebhookReplay` resolves a refused delivery to the trigger
  it collided with. `controller.ParseGitHubWebhookConfig` is the parser for the
  environment document, moved out of the controller binary.
- **Why:** A secret shared by every repository proves only that some holder
  signed the body, and a replay key the sender picks and nothing signs is not a
  replay key at all.

## Service discovery and trigger submission take a closer look

- **Before:** `GET /api/v1/services` answered anyone who could reach the
  controller, `POST /api/v1/triggers` stored `git.repo_url` and every
  trigger environment key it was handed, and `?limit=` on the run list was
  unbounded.
- **After:** Service discovery takes any valid bearer, which every client
  that consumes it already holds. A trigger's `git.repo_url` goes through
  the clone-URL rules the Git cache routes use, so a local path, a
  loopback or private address, or a URL carrying credentials is rejected
  with 400. Trigger environment keeps only `GITHUB_REPOSITORY`, the GitHub
  pull-request context, and the `SPARKWING_START_AT`, `SPARKWING_STOP_AT`,
  `SPARKWING_ONLY`, `SPARKWING_DRY_RUN`, and `SPARKWING_NO_CACHE`
  switches, with `GITHUB_REPOSITORY` accepted only as an `owner/name`
  slug. A caller without `admin` cannot submit `trigger.source: github`
  or the pull-request keys the commit-status reporter trusts; the
  HMAC-verified webhook still can. `?limit=` is capped at 1000 rows on
  the run, trigger, and event lists.
- **Migration:** A hand-rolled client that polls `/api/v1/services`
  anonymously sends `Authorization: Bearer <token>`. One that submits
  triggers carrying its own environment keys moves that data into pipeline
  args, and one that passes a clone URL in `GITHUB_REPOSITORY` passes the
  slug and puts the URL in `git.repo_url`. A client that submitted
  `trigger.source: github` to drive commit statuses needs an `admin`
  token or the webhook. A dashboard or export that asked for more than
  1000 runs, triggers, or events in one request pages instead.
- **Why:** The announcement names internal cache and logs URLs, the
  repository URL becomes a clone target on every runner, the trigger
  environment is served whole to every `triggers.read` principal and is
  where a local retry reads the repository directory it trusts, and a
  single unbounded list request loads every run row with its plan and args
  blobs.

## Token prefixes are unique

- **Before:** Two tokens could carry the same 12-character prefix.
  `sparkwing cluster tokens revoke --prefix` revoked every matching row and
  only then reported the ambiguity, and the read behind rotation returned
  whichever row the store listed first.
- **After:** Run-store schema 26 makes the `idx_tokens_prefix` index unique and
  minting retries on a collision. Revoking and rotating each run in a
  transaction that commits only when exactly one row matched, so an ambiguous
  prefix leaves every token live and a revoke that lands while a rotation is
  minting the replacement is no longer undone.
  `store.LookupTokenByPrefix` returns an error rather than the first of
  several rows, which is what `store.RotateToken` reports as well.
- **Migration:** Upgrade the controller before the runners; older binaries
  refuse the upgraded SQL store. A database that already holds two tokens on
  one prefix fails to open and names the prefix. Revoked rows are kept for
  audit, so the pair can be historical; list it with

  ```sql
  SELECT prefix, hash, principal, revoked_at FROM tokens
   WHERE prefix IN (SELECT prefix FROM tokens
                     GROUP BY prefix HAVING COUNT(*) > 1);
  ```

  then delete the revoked duplicates, keep the one live row, and upgrade. The
  controller cannot open the database until the prefix is unique, so run the
  query against the file with `sqlite3` or against the server with `psql`.
- **Why:** Revoke and rotate are the operator's emergency tools, and neither
  should touch a token other than the one named.
