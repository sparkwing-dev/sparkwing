# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

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

## Node claims bind to the claiming principal

- **Before:** Any `nodes.claim` token could write any node of any run, stamp
  `ready_at` on a node whose dependencies had not finished, and read any run's
  plaintext secret arguments through `?include=secret_values`. Scope gated the
  route; nothing gated the object.
- **After:** `POST /api/v1/nodes/claim` records the authenticated principal
  alongside `holder_id`. The per-node write routes (`activity`, `touch`,
  `annotations`, `summary`, `artifact-manifest`, `metrics`, `dispatch`,
  `steps/*`, `bounce/consume`, `revoke-ready`) answer `403` with
  `"error": "claim_required"` unless the caller holds that node's unexpired
  claim, and `heartbeat` answers `409` unless both the principal and the holder
  id match. `mark-ready` requires `admin`. The execution view returns plaintext
  arguments to an `admin` principal, or to a `nodes.claim` principal holding an
  unexpired claim on one of the run's nodes; a controller serving
  unauthenticated returns the redacted view.
- **Migration:** Give the token that dispatches nodes to a warm pool `admin`,
  which it already needs to create, start, and finish nodes. Pool runners that
  claim their own work keep `nodes.claim`. Mint a token on a controller that
  serves unauthenticated if its runners fetch secret arguments over HTTP.
  Claims taken before the upgrade carry no principal, so a runner in flight
  during the upgrade loses its lease and the node is requeued.
- **Why:** Every laptop agent and pool replica holds a runner token. A token
  scoped to claim work should not read another repository's deploy credentials
  or force a node to run before its dependencies finish.

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
