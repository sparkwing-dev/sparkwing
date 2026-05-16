# Execution Model

Where a pipeline runs, what it acts upon, where its work physically executes, where its configuration and secrets come from, and where its artifacts, logs, and state are persisted — modeled as orthogonal axes with a single, predictable resolution rule.

## Why this exists

Today a sparkwing pipeline is dispatched against a `--on <profile>` (a controller URL) and inherits "am I local?" from environment-variable detection (`SPARKWING_HOST=cluster`, `KUBERNETES_SERVICE_HOST`). That single binary collapses several distinct concerns into one:

1. Which controller orchestrates the run.
2. Which runner physically executes each unit of work.
3. Which logical environment the pipeline acts on.
4. Where configuration values and secrets come from.
5. Where artifacts, logs, and run state are persisted.

The collapse forces pipeline authors into `if Runtime().IsLocal { ... }` branches scattered across business logic, and it leaves no clean way to express things like "this build needs Windows," "this deploy is from my laptop but the secrets live in the team vault," or "in CI we want logs in s3 but locally I want them on disk."

This design separates those concerns, gives each one a typed declaration site, and defines a single resolution rule the scheduler runs before any work begins. **Branching doesn't disappear** — sometimes you really do need two code paths — but it moves out of pipeline business logic and into one-per-concern typed adapters that read the orchestrator's signals (runner type, target, source, backend) instead of sniffing the environment.

## Design decisions

Four choices are load-bearing and shouldn't drift later.

1. **Targets are declared as a map under each pipeline**, with per-target overrides for runners, approvals, source bindings, and config values. A pipeline without a `targets:` block legitimately has no concept of target (e.g. `lint`, `scaffold`); a pipeline with one target auto-selects it.
2. **Runners are matched by labels.** "Local" and "remote" disappear as enum values. A `runners.yaml` declares named runners (templates for cluster-backed types, bindings for static machines, plus the implicit local one) that advertise labels and carry their backing spec; jobs declare label requirements and preferences via three verbs: `Requires`, `Prefers`, `WhenRunner`.
3. **Defaults are explicit per controller.** Each profile in `profiles.yaml` names its `default_runner`. The scheduler uses that default only when a job hasn't picked a preference and the resolved allow-set has multiple valid choices. Ambiguity without a default fails at validation, not silently.
4. **Single-controller per run for v1.** A run uses one orchestrating controller; every runner it touches must be reachable from that controller. Cross-controller and peered dispatch are deferred.

## The axes

| Axis | Question it answers | Declared in | Picked by |
|---|---|---|---|
| Orchestration host | Where does the run record live and who drives dispatch? | `profiles.yaml` | `--on <profile>` (default: configured default profile) |
| Runner | Which runner pool actually executes each job? | `runners.yaml` + per-job `Requires`/`WhenRunner`/`Prefers` | resolution rule, per job |
| Target | What logical environment does this run act on? | `pipelines.yaml` `targets:` block | `--for <target>` (default: pipeline default) |
| Source (config + secrets) | Where do dynamic values come from? | per-target `source:` | resolved at run start from the target |
| Backend (cache, logs, state, binaries) | Where are artifacts, logs, and run state persisted? | `backends.yaml` + environment auto-detect | resolved at process start from the current environment |

The axes are independently selectable. A run can orchestrate on the laptop's local controller, dispatch its build job to a cloud Linux runner and its publish job to a cloud Windows runner, target the staging environment, pull secrets from a shared vault, write logs to an s3 bucket, and store run state in remote Postgres — all without any pipeline code branching on "local vs remote."

## Targets

A target is a named logical environment a pipeline can act on. Targets live in `pipelines.yaml` as a map under the pipeline; each entry may carry its own overrides.

```yaml
pipelines:
  - name: release
    entrypoint: Release
    runners: [local, cloud-linux]     # pipeline-wide allowlist (default for all jobs)
    targets:
      dev:
        values: { replicas: 1 }
      staging:
        values: { replicas: 3 }
      prod:
        runners: [prod-builders]      # narrows the pipeline allowlist for this target
        approvals: required
        protected: true
        values: { replicas: 5 }
      pi:
        runners: [local]
        source: local-keychain
        values: { device_serial: ABCD1234 }
```

Per-target overrides:

- `runners: [...]` — narrows the pipeline-level runner allowlist. Intersection with `pipelines.runners`. Omitted means inherit the pipeline allowlist.
- `source: <name>` — which entry in `sources.yaml` resolves `sparkwing.Secret` / `sparkwing.Config` calls for this target. Defaults to the pipeline's default source (or the global default in `sources.yaml`).
- `approvals: required` — runs against this target pause for a human gate before any jobs dispatch.
- `protected: true` — refuses non-default-branch sources; flags the run loudly in the dashboard.
- `values: { ... }` — overlays values onto the pipeline's typed config struct (see Static configuration below).
- `backend: { ... }` — rare; overrides cache/logs/state destination for runs targeting this environment.

Pipelines may declare zero targets. A pipeline without a `targets:` block rejects `--for` at the CLI and runs with the pipeline-level config only. A pipeline with exactly one target auto-selects it; `--for <name>` is accepted but redundant.

## Runners

A runner is a named entry in `runners.yaml`. Each declares the labels it advertises and, for cluster-backed types, the spec used to materialize runner pods. The label model is shared with `Job.Requires(labels...)`: jobs declare label requirements; runners advertise labels they satisfy.

The runner's name is itself an implicit label, so `Requires("cloud-gpu")` matches the runner of that name, and `Requires("gpu", "linux")` matches any runner advertising both labels. Match semantics are AND across listed labels.

```yaml
# .sparkwing/runners.yaml         (checked in, team-shared)
# ~/.config/sparkwing/runners.yaml (per-user additions, merged)

runners:
  local:
    type: local
    labels: [local, "os=darwin", "arch=arm64"]

  cloud-linux:
    type: kubernetes
    controller: shared
    labels: [cloud-linux, "os=linux", "arch=amd64"]
    spec:
      nodeSelector: { karpenter.sh/nodepool: general }
      resources: { requests: { cpu: 2, memory: 4Gi } }

  cloud-gpu:
    type: kubernetes
    controller: shared
    labels: [cloud-gpu, gpu, "gpu=nvidia-a100", "os=linux"]
    spec:
      nodeSelector: { karpenter.sh/nodepool: gpu-a100 }
      tolerations: [{ key: nvidia.com/gpu, operator: Exists }]
      resources: { requests: { cpu: 4, memory: 16Gi, "nvidia.com/gpu": 1 } }

  cloud-windows:
    type: kubernetes
    controller: shared
    labels: [cloud-windows, "os=windows", "arch=amd64"]
    spec:
      nodeSelector: { kubernetes.io/os: windows }
      resources: { requests: { cpu: 4, memory: 8Gi } }

  prod-builders:
    type: kubernetes
    controller: prod
    labels: [prod-builders, "os=linux", "scope=prod"]
    spec:
      nodeSelector: { karpenter.sh/nodepool: prod-builders }
      tolerations: [{ key: scope, value: prod, operator: Equal }]

  mac-mini-corner:
    type: static                       # a specific machine that registers itself
    labels: [mac-mini-corner, "os=macos", "arch=arm64", trusted]
```

The local runner on a developer's machine is implicit on every CLI installation; declaring it is optional and only needed to attach extra labels. Static runners register themselves with the controller at startup and advertise their labels then — no template needed unless other entries want to address them by name.

### Runner types

| Type | Meaning | Spec block |
|---|---|---|
| `local` | In-process, on whichever host runs the CLI or controller | none |
| `kubernetes` | Pod materialized by a Kubernetes runner pool on the named controller | `spec.nodeSelector`, `spec.tolerations`, `spec.resources` |
| `static` | A long-lived runner process that registers itself; no template | none beyond labels |

Future types (`docker`, `ec2-fleet`, `nomad`) follow the same shape: typed `spec:` block, name + label set unchanged. New backends are added as new `type` values without changing pipeline-author syntax.

### Karpenter and node-pool routing

For Kubernetes-backed runners, node-pool routing is fully expressed in the runner's `spec:` block. Pipeline authors declare what they need by runner name or label; cluster admins decide which Karpenter `NodePool` backs each runner by editing one entry in `runners.yaml`. Swapping A100s for H100s is a single-line edit; no pipeline changes.

Adding a new pool — a Graviton ARM pool, a spot-priced burst pool, a per-team isolated pool — is a new runner entry with the appropriate `nodeSelector` and `tolerations`, plus labels that describe its capabilities.

### Pinning to a specific machine

If a job must run on exactly one physical machine — a dedicated build box, the laptop with the USB-attached hardware — declare it as a `static` runner with a unique label, and `Requires` that label:

```go
sparkwing.Job(plan, "flash-pi", &FlashPi{}).Requires("laptop=korey")
```

The static runner's label set is the contract. No separate machine-pinning API.

## Profiles

Profiles describe controllers — the orchestration hosts a CLI can dispatch to.

```yaml
# ~/.config/sparkwing/profiles.yaml
default: laptop
profiles:
  laptop:
    controller: http://127.0.0.1:4344
    logs: http://127.0.0.1:4345
    default_runner: local
  shared:
    controller: https://shared.example.dev
    token: swu_...
    default_runner: cloud-linux
  prod:
    controller: https://prod.example.com
    token: swu_...
    default_runner: prod-builders
```

`default_runner` is the runner the scheduler picks when a job has no `Prefers` and the resolved allow-set contains more than one valid choice. Without it, ambiguous cases fail at validation with a message naming the candidates.

## RuntimeConfig

Process-lifetime facts continue to live on `RuntimeConfig`, accessed via `sparkwing.Runtime()` (and `sparkwing.CurrentRuntime()`). The struct is trimmed; the name stays.

```go
type RuntimeConfig struct {
    WorkDir string   // discovered repo root; empty when not inside a project
    Git     *Git     // shared with RunContext.Git
}
```

What changes:

- **`IsLocal` removed.** "Am I local?" is no longer a question the code asks. Pipeline code asks specific capability questions (`term.IsTerminal`, `os.UserConfigDir`, an explicit env-var check if a case is load-bearing) or reads the typed runner via `sparkwing.Runner(ctx)`.
- **`RunID` / `NodeID` removed** — duplicates of `RunContext`; the IDs stay on `RunContext`.
- **`Debug` moved out** into a free function `sparkwing.Debug()` that reads a package-level state set at startup.
- **Env-var detection deleted.** No `SPARKWING_HOST`, no `KUBERNETES_SERVICE_HOST` sniffing. The orchestrator hands every job a typed `Runner` context value at dispatch.

### Inspecting the runner

For the rare case where code needs to know which runner it's on:

```go
r := sparkwing.Runner(ctx)
// r.Name   = "cloud-linux" | "laptop" | "mac-mini-corner" | ...
// r.Type   = "local" | "kubernetes" | "static"
// r.Labels = [...]    // the full advertised label set
```

Job code should prefer asking specific capability questions over branching on `r.Type`. `Runner(ctx)` exists for tooling, diagnostics, and one-per-concern adapter code that legitimately needs to inspect.

## Job-level selection: three verbs

Three chainable methods on `*Job` (and on `*JobGroup`, where they delegate to every member). The renames lock in here: `RunsOn` becomes `Requires`; `*Node` becomes `*Job`; `*NodeGroup` becomes `*JobGroup`. Group delegation follows the existing pattern that `Needs`, `Retry`, `Timeout`, `Cache`, and the others already use.

| Verb | Meaning | When no runner matches |
|---|---|---|
| `Requires(labels...)` | Must run on a runner advertising every label | Run fails at validation |
| `WhenRunner(labels...)` | Run only when a matching runner is available | Job silently skipped |
| `Prefers(labels...)` | Bias preferences within the eligible set | Falls through to default_runner |

Maps to "must / if available / prefer." Each has a single, clear failure mode.

### Label syntax

Each argument is one **term**. Within a term, comma-separated values are alternatives (OR). Across arguments, terms compose with AND. Same syntax works for all three verbs.

- `Requires("os=linux", "arch=amd64")` — runner advertises `os=linux` AND `arch=amd64`.
- `Requires("os=linux,macos")` — runner advertises `os=linux` OR `os=macos`.
- `Requires("os=linux,macos", "arch=amd64")` — `(linux OR macos) AND amd64`.
- `Requires("gpu=nvidia-a100,nvidia-h100")` — either accelerator type.
- `Requires("gpu,fpga")` — bare labels OR-ed; runner has one of these as a plain label.

A runner satisfies a term if any of the comma-separated values matches a label the runner advertises.

```go
// Hard constraint — job only runs on cloud-windows; fails the plan otherwise.
sparkwing.Job(plan, "build-windows", &BuildWindows{}).
    Requires("cloud-windows")

// Soft eligibility — preflight runs locally if a local runner is available;
// silently skipped when dispatched to a remote runner.
sparkwing.Job(plan, "preflight-sso", &CheckSSOLogin{Profile: "sso-dev"}).
    WhenRunner("local")

// Preference — must be a linux runner; prefer cloud-linux but fall through.
sparkwing.Job(plan, "integration-tests", &IntegrationTests{}).
    Requires("os=linux").
    Prefers("cloud-linux")

// Group: every member of the fan-out inherits the constraint.
sparkwing.JobFanOut(plan, "image-builds", images, func(img imageSpec) (string, any) {
    return "build-" + img.Name, &BuildImage{Image: img}
}).
    Requires("cloud-linux")

// Explicit hierarchy with group-level preferences.
checks := sparkwing.GroupJobs(plan, "safety",
    sparkwing.Job(plan, "lint",     &Lint{}),
    sparkwing.Job(plan, "security", &Security{}),
    sparkwing.Job(plan, "test",     &Test{}),
).
    Prefers("cloud-linux")
sparkwing.Job(plan, "deploy", &Deploy{}).Needs(checks)
```

### Preflight checks

`WhenRunner` is the verb for preflight checks — work that's only meaningful in certain runner environments. Make the check its own job; downstream depends on it; when the eligibility doesn't hold, the job is skipped and `Needs` treats it as satisfied.

```go
preflight := sparkwing.Job(plan, "preflight-sso", &CheckSSOLogin{Profile: "sso-dev"}).
    WhenRunner("local")

sparkwing.Job(plan, "build", &Build{}).Needs(preflight)
```

When the run lands on a local runner, the preflight runs and can fail fast with `"not logged in — run aws sso login --profile sso-dev"`. When it lands on `cloud-linux`, the preflight is silently skipped and `build` proceeds without it.

### Workable-declared requirements

A Workable struct can declare its own constraints without the plan stating them at registration. Useful for jobs whose constraints are intrinsic to the work, especially in fan-out generators:

```go
type BuildWindows struct{}
func (BuildWindows) Run(ctx context.Context) error { /* ... */ }
func (BuildWindows) Requires() []string { return []string{"cloud-windows"} }
```

The orchestrator reads these methods when wrapping the Workable into a `*Job`. Constraints stated at the registration site take precedence on conflict; the Workable's own declaration is the floor.

### Heterogeneous fan-out

For dynamic fan-out where instances need different constraints, the Workable's own `Requires()` is the cleanest mechanism — each generated job carries its own contract:

```go
type BenchShard struct {
    Spec ShardSpec
}
func (b BenchShard) Run(ctx context.Context) error { /* ... */ }
func (b BenchShard) Requires() []string {
    if b.Spec.NeedsUSB {
        return []string{"local"}
    }
    return []string{"cloud-linux"}
}

sparkwing.JobFanOutDynamic(plan, "bench", shardSource, func(shard ShardSpec) (string, any) {
    return "bench-" + shard.Name, BenchShard{Spec: shard}
})
```

## Resolution rule

For each job (and each fan-out instance independently), the scheduler computes:

```
1. allowed = pipeline.runners
           ∩ (target.runners      if set, else all)
           ∩ (job.Requires        if set, else all)
           ∩ (workable.Requires() if implemented, else all)

2. if job has WhenRunner labels and no runner in allowed matches them:
       mark job skipped; downstream Needs treats it as satisfied
   else:
       chosen = first match of job.Prefers within allowed
              else profile.default_runner if it is in allowed
              else error: ambiguous; require an explicit choice

3. CLI overrides (--prefer, --job <id>=<runner>) bias step 2 but
   must keep chosen ∈ allowed. Overrides that would violate a hard
   rail are rejected at parse time, not silently ignored.
```

The whole decision produces either one runner per job, a skip mark, or a clear error at run start — before any work dispatches.

## Static configuration: typed values, layered overlays

Static configuration — values code-reviewed in the repo — flows through a typed Go struct declared by the pipeline. YAML supplies the values; the struct supplies the types.

```go
type ReleaseConfig struct {
    ImageRepo string `sw:"image_repo,required"`
    Replicas  int    `sw:"replicas"  default:"2"`
    Region    string `sw:"region"    default:"us-west-2"`
}

type Release struct{}

func (Release) Config() any { return &ReleaseConfig{} }

func (Release) Plan(ctx context.Context, plan *sparkwing.Plan, in Inputs, rc sparkwing.RunContext) error {
    cfg := sparkwing.PipelineConfig[ReleaseConfig](ctx)
    build := sparkwing.Job(plan, "build", &Build{Repo: cfg.ImageRepo})
    sparkwing.Job(plan, "deploy", &Deploy{
        Replicas: cfg.Replicas,
        Region:   cfg.Region,
    }).Needs(build)
    return nil
}
```

```yaml
pipelines:
  - name: release
    entrypoint: Release
    values:
      base: { image_repo: example.dev/api }       # applied to every target
    targets:
      dev:     { values: { replicas: 1 } }
      staging: { values: { replicas: 3 } }
      prod:    { values: { replicas: 5 }, runners: [prod-builders] }
      pi:      { values: { device_serial: ABCD1234 }, runners: [local] }
```

Layers applied in order; later layers win per field:

1. Pipeline `values.base`
2. `targets.<selected>.values`
3. Per-runner overlay if declared (rare): `values.runners.<name>`
4. CLI flags from typed `Inputs`

Resolution happens once at run start. The resolved struct is attached to ctx; jobs read it via `sparkwing.PipelineConfig[T](ctx)`. Missing `required` fields fail before any job dispatches, with a message naming the field and the layer it was expected from.

`sparkwing run release config --for staging` prints the resolved struct plus the source of each field.

## Dynamic configuration and secrets

`sparkwing.Secret(ctx, name)` and `sparkwing.Config(ctx, name)` resolve through a `SecretResolver` installed on ctx. This design keeps that surface and adds explicit, per-target source selection.

```yaml
# .sparkwing/sources.yaml
default: team-vault

sources:
  team-vault:
    type: remote-controller
    controller: shared
  prod-vault:
    type: remote-controller
    controller: prod
  local-keychain:
    type: macos-keychain
    service: sparkwing-pi
  dotenv:
    type: file
    path: .sparkwing/secrets.local.env
```

Each target binds to a source:

```yaml
targets:
  dev:     { source: team-vault }
  staging: { source: team-vault }
  prod:    { source: prod-vault, runners: [prod-builders] }
  pi:      { source: local-keychain, runners: [local] }
```

At run start the orchestrator picks the source from `targets[selected].source` (falling back to `sources.default`), builds a resolver bound to it, and installs it on ctx via the existing `sparkwing.WithSecretResolver`. Job bodies stay unchanged:

```go
func (d *Deploy) Run(ctx context.Context) error {
    dbURL, err := sparkwing.Secret(ctx, "DATABASE_URL")
    if err != nil { return err }
    region, _ := sparkwing.Config(ctx, "REGION")
    // ...
}
```

The same call hits the team vault when targeting staging from a laptop, hits the prod vault when targeting prod, and hits the macOS keychain when targeting pi. The pipeline never knows which.

### Fail-fast for required secrets

A pipeline can opt into eager resolution by declaring a secrets struct alongside its config:

```go
type ReleaseSecrets struct {
    DeployToken string `sw:"DEPLOY_TOKEN,required"`
    SlackHook   string `sw:"SLACK_HOOK"`
}

func (Release) Secrets() any { return &ReleaseSecrets{} }
```

At run start the orchestrator resolves every `required` entry against the chosen source and fails the run loudly if any are missing — before the first job dispatches.

### Same target, different credentials

`release --for prod` and `investigate-prod --for prod` both target prod but legitimately need different IAM roles. They declare different secret names (`DEPLOY_TOKEN` vs `READ_TOKEN`); the same vault returns different values for each. Pipeline identity is the namespace.

## Storage backends

Cache (content-addressed artifacts including compiled pipeline binaries), logs (per-job log streams), and state (the run-record store) each have a pluggable backend. Where they live depends on the environment — GHA wants s3, cluster mode wants the controller's hosted services, laptop wants the local filesystem — and any default can be overridden.

```yaml
# .sparkwing/backends.yaml (checked in)
# ~/.config/sparkwing/backends.yaml (per-user overlay)

defaults:
  cache:
    type: filesystem
    path: ~/.cache/sparkwing
  logs:
    type: filesystem
    path: ~/.cache/sparkwing/logs
  state:
    type: sqlite
    path: ~/.cache/sparkwing/state.db

environments:
  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    cache:
      type: s3
      bucket: sparkwing-cache
      prefix: ${GITHUB_REPOSITORY}/
    logs:
      type: s3
      bucket: sparkwing-logs
      prefix: ${GITHUB_REPOSITORY}/
    state:
      type: s3
      bucket: sparkwing-state
      prefix: ${GITHUB_REPOSITORY}/

  kubernetes:
    detect: { env_var: KUBERNETES_SERVICE_HOST, present: true }
    cache: { type: controller }
    logs:  { type: controller }
    state: { type: postgres, url_source: state_db_url }

  cluster-shared:                       # manually selected via --backends-env
    cache: { type: s3, bucket: team-cache }
    logs:  { type: s3, bucket: team-logs }
    state: { type: postgres, url_source: state_db_url }
```

### Backend types

| Surface | Types | Use |
|---|---|---|
| `cache` | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller` | Content-addressed artifact and compiled-binary store |
| `logs` | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller`, `stdout` | Per-job log stream persistence |
| `state` | `sqlite`, `postgres`, `mysql`, `controller` | Run records, plan snapshots, status |

Adding a new backend type is a new `type:` value with a typed spec block; pipeline-author and orchestrator code stay unchanged because everything routes through the same interfaces (`sparkwing.Cache`, `sparkwing.Logs`, `sparkwing.State`).

### Selection precedence

First match wins:

1. CLI flag (`--backends-env <name>`) — explicit override.
2. Target-level overlay (`targets.<name>.backend: { ... }`) — rare.
3. Environment auto-detect — first `environments:` entry whose `detect:` block evaluates true.
4. `defaults:` block — the fallback.

### Pipeline binary distribution

A compiled pipeline binary is one of two things:

1. A cache entry under `cache.bin/<hash>` — the orchestrator fetches and execs without recompiling on a hit. Hash is over the resolved sparks set + pipeline source.
2. A fresh compile from the working tree — happens on cache miss, then publishes the result back to `cache.bin/<hash>` for next time.

Because compiled binaries live in the cache backend, the existing backend selection covers them. In CI you point `cache` at an s3 bucket and every run skips compilation if a previous build already populated `bin/<hash>`. Locally you point it at the filesystem and rebuild only when source changes.

An optional `cache.binaries` subspace isolates them for teams that want a shared binary cache while keeping local cache on disk:

```yaml
defaults:
  cache:
    type: filesystem
    path: ~/.cache/sparkwing
    binaries:
      type: s3
      bucket: sparkwing-binaries
      prefix: ${PIPELINE_NAME}/
```

### Per-target backend override

For the rare case where a target needs a different destination (prod runs write logs to a prod-only bucket for audit), a `backend:` overlay on the target wins over the environment default:

```yaml
targets:
  prod:
    runners: [prod-builders]
    source: prod-vault
    backend:
      logs:  { type: s3, bucket: prod-audit-logs, prefix: ${RUN_ID}/ }
      state: { type: postgres, url_source: prod_state_db }
```

## Writing adapters for genuinely-different code paths

When data alone doesn't cover the difference — kubectl vs client-go, SSO browser flow vs IRSA — write a one-per-concern adapter that branches on a typed signal in one place. Pipeline business logic stays clean; the branching lives in the adapter.

### AWS auth — usually data-only, no branching needed

When the only thing that varies is the profile name (or region, or endpoint), let the AWS SDK's credential chain do the dispatch:

```go
func (d *Deploy) Run(ctx context.Context) error {
    profile, _ := sparkwing.Config(ctx, "aws_profile")  // "sso-dev" locally, "" in cluster
    cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile))
    // AWS SDK falls through to IRSA when profile == ""
    // ...
}
```

The source bound to `target=dev` returns `"sso-dev"` from the laptop's local-keychain; the source bound to the cluster returns `""`. Same job code; the typed config flows the difference. No branching.

### Kubernetes client — genuinely two code paths

kubectl shell-out and client-go are different code. Branch in one place:

```go
// mykube/client.go  (in your project, not in sparkwing)
type Client interface {
    GetPods(ctx context.Context, ns string) ([]Pod, error)
    // ...
}

func New(ctx context.Context) (Client, error) {
    r := sparkwing.Runner(ctx)
    if r.HasLabel("local") {
        return &kubectlClient{kubeconfig: kubeconfigPath()}, nil
    }
    return &apiClient{}, nil   // in-cluster client-go using service account
}
```

```go
// pipeline body
func (d *Deploy) Run(ctx context.Context) error {
    kc, err := mykube.New(ctx)
    if err != nil { return err }
    pods, err := kc.GetPods(ctx, "default")
    // ...
}
```

The branching still exists — but:

1. **One location.** `mykube/client.go`. Not sprinkled across 12 job bodies.
2. **Typed signal.** `r.HasLabel("local")` reads a label set on a typed `Runner` value the orchestrator installed at dispatch. Not `os.Getenv("KUBERNETES_SERVICE_HOST")`.
3. **Driven by the declared topology.** The signal reflects which runner the scheduler picked, not heuristics about the environment. Running the same pipeline inside docker on your laptop won't accidentally trip the "remote" path.
4. **Testable.** Construct a context with `sparkwing.WithRunner(ctx, Runner{Labels: []string{"kubernetes"}})` and exercise the in-cluster branch from your laptop.
5. **Pipeline business logic stays clean.** `Run` reads as the business intent; the runtime decision is one level down.

### Preflight checks — see Job-level selection

Preflight work that's only meaningful in some runner environments lives as its own job with `WhenRunner`. The DAG carries the eligibility condition; no branching needed.

## Triggers do not force runtime

Triggers — `manual`, `push`, `schedule`, `webhook`, `pre_commit`, `pre_push` — register the pipeline at whichever controller holds them. Where the trigger fires has no direct bearing on which runner runs the work. A webhook can fire on a laptop's local controller (via ngrok) and dispatch its work to cloud-linux; a scheduled run can fire on the shared controller and dispatch work to the local runner of a registered worker.

The only coupling is practical: scheduled triggers require a controller with scheduling enabled. That fact belongs in helptext, not in a hardwired runtime constraint.

## CLI surface

```bash
# Simple cases default cleanly: laptop CLI + default target + default runner.
sparkwing run lint
sparkwing run release --for dev

# Cross-controller dispatch (orchestrate on a different controller).
sparkwing run release --on shared --for staging
sparkwing run release --on prod   --for prod

# Per-job runner override (must satisfy the job's Requires).
sparkwing run release --for staging --job build=cloud-linux

# Bias preferences across the run.
sparkwing run integration-tests --prefer local

# Force a backend environment (overrides auto-detect).
sparkwing run release --for dev --backends-env cluster-shared

# Introspect resolved plan + runner choices + sources before pressing go.
sparkwing run release --for staging --plan
sparkwing run release config --for staging
```

Autocomplete:

- `sparkwing run <TAB>` — pipelines whose `runners:` intersects the current profile's `default_runner` (or all, if no default).
- `sparkwing run <pipeline> --for <TAB>` — the pipeline's declared targets.
- `sparkwing run <pipeline> --on <TAB>` — profiles whose `default_runner` is in the pipeline's allowed set.
- `sparkwing run <pipeline> --job <TAB>` — job IDs from the resolved plan; `=<TAB>` offers runners satisfying that job's `Requires`.
- `sparkwing run <pipeline> --backends-env <TAB>` — entries from `backends.yaml` `environments:`.

## Test scenarios

Each scenario is a concrete pipeline shape the design must support, with the constraint it tests.

| Pipeline | What it tests |
|---|---|
| `release` (multi-target with prod-from-remote-only) | Per-target runner narrowing; approvals on prod; layered values per target |
| `release-pi` (single-target, local-only, local keychain) | Single-target auto-selection; `source: local-keychain`; static-runner pinning by unique label |
| `lint` (no target, runs anywhere) | Pipelines without a `targets:` block; `--for` rejected; CLI defaults pick cleanly |
| `migrate-db` (remote-only, prod approval) | `runners:` excludes local; per-target approval gate fires before any job dispatches |
| `investigate-prod` (read-only against prod) | Same target as `release` but different secret names; vault returns different values |
| `webhook-deploy` (target from payload) | Webhook trigger picks `--for` value from payload; rejected when payload target is outside the pipeline's allowed set |
| `revert-deploy --emergency` (orthogonal modifier) | CLI flag bypasses approval gate; run logs an "emergency override" record |
| `train-model` (GPU pool) | `Requires("cloud-gpu")` routes to the Karpenter A100 pool; pod scheduled with correct tolerations/resources |
| `report-weekly` (scheduled) | Schedule registered on shared controller fires; run dispatches to the controller's `default_runner` |
| `scaffold-pipeline` (local-only, no target) | Local-only, no `targets:`, no `source:` resolution needed |
| `build-windows` inside `release` (step-level offload) | Local CLI orchestrates; the `build-windows` job alone routes to `cloud-windows` while peers stay local |
| `preflight-sso` with `WhenRunner("local")` | Runs and may fail fast when on local runner; silently skipped on cloud-linux; `Needs` treats skip as satisfied |
| `release --for pi` from laptop | Orchestration on laptop; secrets from macOS keychain; deploy job runs on local runner |
| `release --for staging` from laptop | Orchestration on laptop; secrets from team vault (remote fetch); deploy job runs on cloud-linux |
| GHA-driven smoke run | `backends.yaml` `environments.gha` auto-detects on `GITHUB_ACTIONS=true`; cache/logs/state land in s3; compiled binary fetched from `cache.bin/<hash>` |
| Cluster-mode run (controller dispatch) | `KUBERNETES_SERVICE_HOST` auto-detect; logs and cache route via controller; runner pod materialized from runner `spec.nodeSelector` |
| Specific-machine pinning | Static runner declared with a unique label (`laptop=korey`); `Requires("laptop=korey")` only schedules on that machine; other runners ignored |
| Karpenter pool swap | A100 pool replaced with H100 by editing one runner entry; no pipeline code changes; next run schedules onto new node type |
| Heterogeneous fan-out | `JobFanOutDynamic` produces Workables that declare their own `Requires()`; each instance routes independently |
| Group-level requirement propagation | `GroupJobs(...).Requires("cloud-linux")` applies to all members; `Prefers` and `WhenRunner` propagate the same way |
| Adapter-driven kubectl/client-go branch | `mykube.New(ctx)` reads `sparkwing.Runner(ctx).HasLabel("local")` and returns the right implementation; pipeline body unchanged |

## What this replaces

**`RuntimeConfig.IsLocal` and the `SPARKWING_HOST` / `KUBERNETES_SERVICE_HOST` env-var detection.** `RuntimeConfig` is trimmed in place: `WorkDir` and `Git` remain. `IsLocal`, `RunID`, `NodeID`, env-var-driven mode detection are removed. `Debug` moves to a free function `sparkwing.Debug()`. Pipeline code reads typed config, calls `sparkwing.Secret/Config`, declares runner constraints, and inspects `sparkwing.Runner(ctx)` only inside one-per-concern adapters.

**The `Venue` enum** (`VenueEither`, `VenueLocalOnly`, `VenueClusterOnly`). Subsumed by the pipeline-level `runners:` allowlist plus per-target `runners:` overrides. The `Venue() sparkwing.Venue` optional method on pipeline values is removed.

**`TriggerInfo.Env`** (the untyped string map carried on `RunContext.Trigger`). Trigger-supplied values now decode into the pipeline's typed config via a `trigger:` overlay in `pipelines.yaml`:

```yaml
push:
  branches: [main]
  values: { deploy_env: staging }
```

Job code reads `cfg.DeployEnv`, not `rc.TriggerEnv("DEPLOY_ENV")`. `TriggerInfo.Env` is retained read-only for one release, then removed.

**The `secrets:` list on `Pipeline`** (currently a no-op backward-compat field). Reactivated as the fail-fast list, populated automatically from the `Secrets()` typed struct's `required` fields. Free-form string lists are no longer accepted.

**`SPARKWING_LOG_STORE` and `SPARKWING_ARTIFACT_STORE`** env vars. Replaced by `backends.yaml` with auto-detection. A compatibility shim maps the old env vars to the new structure for one release; deprecation surfaces a warning when the legacy form is detected.

## Out of scope for v1

- **Cross-controller dispatch.** A run uses one orchestration controller; every runner it touches must be reachable from that controller.
- **Custom backend types beyond the listed cloud providers.** Type-discriminated structure is in place; concrete backends land as demand surfaces.
- **Per-secret source override.** Today a target picks one source.
- **Runtime runner advertisement updates.** Runners are loaded at process start; hot-reload of `runners.yaml` is not supported in v1.

## Migration

Each step is a self-contained PR. Existing pipelines run unchanged through step 8; step 10 is the only one that surfaces compile-time deprecation noise.

1. **Rename `*Node` → `*Job`, `*NodeGroup` → `*JobGroup`, `RunsOn` → `Requires`.** Pure type/method rename; behavior unchanged. SDK godoc examples updated; Workable struct naming convention drops the "Job" suffix in examples.
2. **Add `Prefers(labels...)` and `WhenRunner(labels...)`** as peers to `Requires` on `*Job` and `*JobGroup`. Wire preference ordering and skip-on-no-match through the scheduler.
3. **Add `Workable.Requires() []string` and `Workable.Prefers() []string`** as optional interfaces; orchestrator reads them when wrapping a Workable into a `*Job`.
4. **Add `runners.yaml`** parser + merge logic. Define `type: local`, `type: kubernetes`, `type: static`. The local runner remains implicit.
5. **Add `default_runner`** to `profiles.yaml`. Default it to `local` for any profile without one.
6. **Extend `pipelines.yaml`** with `targets:`, `runners:`, `values:`, and lift `secrets:` semantics. Keep parsing the old `secrets: []string` form for one release.
7. **Add optional `Config()` / `Secrets()` methods** to pipelines. Pipelines without them keep working; pipelines with them get the layered typed config + fail-fast resolution.
8. **Add `sources.yaml`** parser and resolver-by-target wiring. Add `backends.yaml` parser, auto-detect rules, and the `Cache` / `Logs` / `State` interfaces. Compatibility shim for `SPARKWING_LOG_STORE` / `SPARKWING_ARTIFACT_STORE`.
9. **CLI surface**: `--for`, `--job <id>=<runner>`, `--prefer`, `--backends-env`, autocomplete updates, `sparkwing run <pipeline> config` introspection.
10. **Trim `RuntimeConfig` in place.** Remove `IsLocal`, `RunID`, `NodeID`, and env-var-based detection. Move `Debug` to a free function `sparkwing.Debug()`. Add `sparkwing.Runner(ctx)` accessor (with `WithRunner` constructor for tests). Delete the `Venue` enum and its optional method on pipeline values.
11. **Deprecate** `TriggerInfo.Env` and the env-var compatibility shims with build-time warnings. Remove one release later.
