# Execution Model

> **Status:** historical design record, not a description of current behavior.
> Project config now lives in a single
> `.sparkwing/sparkwing.yaml`; the standalone `runners.yaml`,
> `sources.yaml`, and `backends.yaml` files this document assumes are
> rejected by the loader. See docs/config-reference.md and
> docs/backends.md for the shipped schema.

Where a pipeline runs, what it acts upon, where its work physically executes, where its configuration and secrets come from, and where its artifacts, logs, and state are persisted -- modeled as orthogonal axes with a single, predictable resolution rule.

## Why this exists

The problem this design attacks: a pipeline dispatched against a single "where" selector (a controller URL) that also decides "am I local?" through environment-variable detection (`SPARKWING_HOST=cluster`, `KUBERNETES_SERVICE_HOST`). That single binary collapses several distinct concerns into one:

1. Which controller orchestrates the run.
2. Which runner physically executes each unit of work.
3. Which logical environment the pipeline acts on.
4. Where configuration values and secrets come from.
5. Where artifacts, logs, and run state are persisted.

The collapse forces pipeline authors into `if Runtime().IsLocal { ... }` branches scattered across business logic, and it leaves no clean way to express things like "this build needs Windows," "this deploy is from my laptop but the secrets live in the team vault," or "in CI we want logs in s3 but locally I want them on disk."

This design separates those concerns, gives each one a typed declaration site, and defines a single resolution rule the scheduler runs before any work begins. **Branching doesn't disappear** -- sometimes you really do need two code paths -- but it moves out of pipeline business logic and into one-per-concern typed adapters that read the orchestrator's signals (runner type, target, source, backend) instead of sniffing the environment.

## Design decisions

Four choices are load-bearing and shouldn't drift later.

1. **A target names the logical environment a run acts on**, selected per run rather than baked into the pipeline's identity. A pipeline that has no concept of target (e.g. `lint`, `scaffold`) simply ignores it.
2. **Runners are matched by labels.** "Local" and "remote" disappear as enum values. Named runners (templates for cluster-backed types, bindings for static machines, plus the implicit local one) advertise labels and carry their backing spec; jobs declare label requirements and preferences via three verbs: `Requires`, `Prefers`, `WhenRunner`.
3. **Defaults are explicit per controller.** Each profile in `profiles.yaml` names its `default_runner`. The scheduler uses that default only when a job hasn't picked a preference and the resolved allow-set has multiple valid choices. Ambiguity without a default fails at validation, not silently.
4. **Single-controller per run for v1.** A run uses one orchestrating controller; every runner it touches must be reachable from that controller. Cross-controller and peered dispatch are deferred.

## The axes

| Axis | Question it answers | Declared in | Picked by |
|---|---|---|---|
| Orchestration host | Where does the run record live and who drives dispatch? | `profiles.yaml` | `--profile <name>` (default: configured default profile) |
| Runner | Which runner pool actually executes each job? | runner declarations + per-job `Requires`/`WhenRunner`/`Prefers` | resolution rule, per job |
| Target | What logical environment does this run act on? | the pipeline's own args | `--target <name>` (default: the pipeline's arg default) |
| Source (config + secrets) | Where do dynamic values come from? | the resolved profile's `secrets:` surface | resolved at run start |
| Backend (cache, logs, state, binaries) | Where are artifacts, logs, and run state persisted? | the resolved profile's `state:` / `cache:` / `logs:` surfaces | resolved at process start from the selected profile |

The axes are independently selectable. A run can orchestrate on the laptop's local controller, dispatch its build job to a cloud Linux runner and its publish job to a cloud Windows runner, target the staging environment, pull secrets from a shared vault, write logs to an s3 bucket, and store run state in remote Postgres -- all without any pipeline code branching on "local vs remote."

## Targets

A target is a named logical environment a pipeline can act on. **The shipped shape is not a config-declared environment map.** A pipeline entry in `.sparkwing/sparkwing.yaml` accepts `name`, `entrypoint`, `description`, `on`, `hidden`, `guards`, `args`, `profile`, and `requires`; the strict parser rejects everything else, including a `targets:` map and per-pipeline `runners:` / `values:` blocks.

What the two declaration sites do instead:

- `requires: [...]` -- the pipeline's runner-label allowlist. Every job in the pipeline must satisfy it in addition to its own `Requires()`. The reserved label `local` pins execution to the in-process runner.
- `profile: <name>` -- binds the pipeline to a project profile, which carries its backend surfaces (`state:`, `cache:`, `logs:`) and its `secrets:` source. The profile applies wholesale; there is no per-surface layering.

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release
    entrypoint: Release
    requires: [cloud-linux]
    profile: prod-audit
    args:
      target: staging
```

`--target <name>` is a runtime value, not a config-declared environment. It flows into the pipeline's own typed args, so the pipeline decides what "staging" means; `args:` supplies its default. A pipeline with no target arg simply ignores the flag.

## Runners

A runner is a named, declared entry. Each declares the labels it advertises and, for cluster-backed types, the spec used to materialize runner pods. The label model is shared with `JobNode.Requires(labels...)`: jobs declare label requirements; runners advertise labels they satisfy.

The runner's name is itself an implicit label, so `Requires("cloud-gpu")` matches the runner of that name, and `Requires("gpu", "linux")` matches any runner advertising both labels. Match semantics are AND across listed labels.

```yaml
# Runner declarations. Project config lives in .sparkwing/sparkwing.yaml
# and per-user config in ~/.config/sparkwing/profiles.yaml; neither
# loader models a standalone `runners:` map (see the status banner).

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

The local runner on a developer's machine is implicit on every CLI installation; declaring it is optional and only needed to attach extra labels. Static runners register themselves with the controller at startup and advertise their labels then -- no template needed unless other entries want to address them by name.

### Runner types

| Type | Meaning | Spec block |
|---|---|---|
| `local` | In-process, on whichever host runs the CLI or controller | none |
| `kubernetes` | Pod materialized by a Kubernetes runner pool on the named controller | `spec.nodeSelector`, `spec.tolerations`, `spec.resources` |
| `static` | A long-lived runner process that registers itself; no template | none beyond labels |

Future types (`docker`, `ec2-fleet`, `nomad`) follow the same shape: typed `spec:` block, name + label set unchanged. New backends are added as new `type` values without changing pipeline-author syntax.

### Karpenter and node-pool routing

For Kubernetes-backed runners, node-pool routing is fully expressed in the runner's `spec:` block. Pipeline authors declare what they need by runner name or label; cluster admins decide which Karpenter `NodePool` backs each runner by editing that runner's entry. Swapping A100s for H100s is a single-line edit; no pipeline changes.

Adding a new pool -- a Graviton ARM pool, a spot-priced burst pool, a per-team isolated pool -- is a new runner entry with the appropriate `nodeSelector` and `tolerations`, plus labels that describe its capabilities.

### Pinning to a specific machine

If a job must run on exactly one physical machine -- a dedicated build box, the laptop with the USB-attached hardware -- declare it as a `static` runner with a unique label, and `Requires` that label:

```go
sparkwing.Job(plan, "flash-pi", &FlashPi{}).Requires("laptop=korey")
```

The static runner's label set is the contract. No separate machine-pinning API.

## Profiles

Profiles describe controllers -- the orchestration hosts a CLI can dispatch to.

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
- **`RunID` / `NodeID` removed** -- duplicates of `RunContext`; the IDs stay on `RunContext`.
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

Three chainable methods on `*JobNode` (the type `sparkwing.Job` returns) and on `*JobGroup`, where they delegate to every member. `RunsOn` is spelled `Requires`; the node types are `*JobNode` and `*JobGroup`. Group delegation follows the existing pattern that `Needs`, `Retry`, `Timeout`, `Cache`, and the others already use.

| Verb | Meaning | When no runner matches |
|---|---|---|
| `Requires(labels...)` | Must run on a runner advertising every label | Run fails at validation |
| `WhenRunner(labels...)` | Run only when a matching runner is available | Job silently skipped |
| `Prefers(labels...)` | Bias preferences within the eligible set | Falls through to default_runner |

Maps to "must / if available / prefer." Each has a single, clear failure mode.

### Label syntax

Each argument is one **term**. Within a term, comma-separated values are alternatives (OR). Across arguments, terms compose with AND. Same syntax works for all three verbs.

- `Requires("os=linux", "arch=amd64")` -- runner advertises `os=linux` AND `arch=amd64`.
- `Requires("os=linux,macos")` -- runner advertises `os=linux` OR `os=macos`.
- `Requires("os=linux,macos", "arch=amd64")` -- `(linux OR macos) AND amd64`.
- `Requires("gpu=nvidia-a100,nvidia-h100")` -- either accelerator type.
- `Requires("gpu,fpga")` -- bare labels OR-ed; runner has one of these as a plain label.

A runner satisfies a term if any of the comma-separated values matches a label the runner advertises.

```go
// Hard constraint -- job only runs on cloud-windows; fails the plan otherwise.
sparkwing.Job(plan, "build-windows", &BuildWindows{}).
    Requires("cloud-windows")

// Soft eligibility -- preflight runs locally if a local runner is available;
// silently skipped when dispatched to a remote runner.
sparkwing.Job(plan, "preflight-sso", &CheckSSOLogin{Profile: "sso-dev"}).
    WhenRunner("local")

// Preference -- must be a linux runner; prefer cloud-linux but fall through.
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

`WhenRunner` is the verb for preflight checks -- work that's only meaningful in certain runner environments. Make the check its own job; downstream depends on it; when the eligibility doesn't hold, the job is skipped and `Needs` treats it as satisfied.

```go
preflight := sparkwing.Job(plan, "preflight-sso", &CheckSSOLogin{Profile: "sso-dev"}).
    WhenRunner("local")

sparkwing.Job(plan, "build", &Build{}).Needs(preflight)
```

When the run lands on a local runner, the preflight runs and can fail fast with `"not logged in -- run aws sso login --profile sso-dev"`. When it lands on `cloud-linux`, the preflight is silently skipped and `build` proceeds without it.

### Workable-declared requirements

A Workable struct can declare its own constraints without the plan stating them at registration. Useful for jobs whose constraints are intrinsic to the work, especially in fan-out generators:

```go
type BuildWindows struct{}
func (BuildWindows) Run(ctx context.Context) error { /* ... */ }
func (BuildWindows) Requires() []string { return []string{"cloud-windows"} }
```

The orchestrator reads these methods when wrapping the Workable into a `*JobNode`. Constraints stated at the registration site take precedence on conflict; the Workable's own declaration is the floor.

### Heterogeneous fan-out

For dynamic fan-out where instances need different constraints, the Workable's own `Requires()` is the cleanest mechanism -- each generated job carries its own contract:

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
1. allowed = pipeline.requires
           ∩ (job.Requires        if set, else all)
           ∩ (workable.Requires() if implemented, else all)

2. if job has WhenRunner labels and no runner in allowed matches them:
       mark job skipped; downstream Needs treats it as satisfied
   else:
       chosen = first match of job.Prefers within allowed
              else profile.default_runner if it is in allowed
              else error: ambiguous; require an explicit choice
```

The whole decision produces either one runner per job, a skip mark, or a clear error at run start -- before any work dispatches.

## Static configuration: typed args, typed secrets

Static configuration -- values code-reviewed in the repo -- flows through the pipeline's typed `Inputs` struct. There is no separate `Config() any` provider and no `values:` layering: the typed struct supplies the field types and defaults, `args:` in `.sparkwing/sparkwing.yaml` supplies per-project defaults, and an explicit CLI flag wins over both.

```go
type ReleaseArgs struct {
    ImageRepo string `flag:"image-repo"`
    Replicas  int    `flag:"replicas" default:"2"`
    Region    string `flag:"region"   default:"us-west-2"`
}

type Release struct{}

func (Release) Plan(ctx context.Context, plan *sparkwing.Plan, in ReleaseArgs, rc sparkwing.RunContext) error {
    build := sparkwing.Job(plan, "build", &Build{Repo: in.ImageRepo})
    sparkwing.Job(plan, "deploy", &Deploy{
        Replicas: in.Replicas,
        Region:   in.Region,
    }).Needs(build)
    return nil
}
```

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release
    entrypoint: Release
    args:
      image-repo: example.dev/api
      replicas: "3"
```

Precedence, lowest to highest: the struct's `default:` tag, the pipeline's `args:` entry, the operator's CLI flag. Resolution happens once at run start, before any job dispatches.

The fail-fast typed surface that *is* declared alongside the pipeline is `Secrets()`; see the next section. `sparkwing run <pipeline> config` prints the declared secrets view -- each field with its provenance and resolution status -- not a resolved config struct.

## Dynamic configuration and secrets

`sparkwing.Secret(ctx, name)` and `sparkwing.Config(ctx, name)` resolve through a `SecretResolver` installed on ctx. The source is the resolved profile's `secrets:` surface, so selecting a profile selects the vault.

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  laptop:
    secrets: { type: filesystem, path: .sparkwing/secrets.local.env }
  shared:
    secrets: { type: controller, url: https://shared.example.dev }
  prod:
    secrets: { type: controller, url: https://prod.example.com }
```

A pipeline pins its own source by naming a project profile:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release
    entrypoint: Release
    profile: prod
```

At run start the orchestrator resolves the profile, builds a resolver bound to its `secrets:` surface, and installs it on ctx via `sparkwing.WithSecretResolver`. Job bodies stay unchanged:

```go
func (d *Deploy) Run(ctx context.Context) error {
    dbURL, err := sparkwing.Secret(ctx, "DATABASE_URL")
    if err != nil { return err }
    region, _ := sparkwing.Config(ctx, "REGION")
    // ...
}
```

The same call hits the team vault under the `shared` profile, hits the prod vault under `prod`, and reads the local dotenv under `laptop`. The pipeline never knows which.

### Fail-fast for required secrets

A pipeline opts into eager resolution by declaring a secrets struct:

```go
type ReleaseSecrets struct {
    DeployToken string `sw:"DEPLOY_TOKEN,required"`
    SlackHook   string `sw:"SLACK_HOOK,optional"`
}

func (Release) Secrets() any { return &ReleaseSecrets{} }
```

At run start the orchestrator resolves every required entry against the chosen source and fails the run loudly if any are missing -- before the first job dispatches. Fields default to required when neither `,required` nor `,optional` is set. Job bodies read the populated struct with `sparkwing.PipelineSecrets[ReleaseSecrets](ctx)`.

### Same target, different credentials

`release --target prod` and `investigate-prod --target prod` both target prod but legitimately need different IAM roles. They declare different secret names (`DEPLOY_TOKEN` vs `READ_TOKEN`); the same vault returns different values for each. Pipeline identity is the namespace.

## Storage backends

Cache (content-addressed artifacts including compiled pipeline binaries), logs (per-job log streams), and state (the run-record store) each have a pluggable backend. Where they live is a property of the selected profile -- CI wants s3, cluster mode wants the controller's hosted services, laptop wants the local filesystem.

```yaml
# ~/.config/sparkwing/profiles.yaml (per-user)
# .sparkwing/sparkwing.yaml         (project, under `profiles:`)

profiles:
  laptop:
    secrets: { type: env }
    cache: { type: filesystem, path: ~/.cache/sparkwing }
    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }
    state: { type: sqlite }

  ci:
    secrets: { type: env }
    cache: { type: s3, bucket: sparkwing-cache, prefix: "${GITHUB_REPOSITORY}/" }
    logs:  { type: s3, bucket: sparkwing-logs,  prefix: "${GITHUB_REPOSITORY}/" }
    state: { type: s3, bucket: sparkwing-state, prefix: "${GITHUB_REPOSITORY}/" }

  cluster:
    secrets: { type: controller, url: https://prod.example.com }
    cache: { type: controller }
    logs:  { type: controller }
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

The resolved profile applies wholesale -- surfaces are not layered in one at a time. First match wins on the profile itself:

1. `--profile <name>` on the command line.
2. The pipeline's own `profile:` key.
3. The project's `defaults.profile` in `.sparkwing/sparkwing.yaml`.
4. The built-in local defaults (sqlite state, filesystem cache and logs).

### Pipeline binary distribution

A compiled pipeline binary is one of two things:

1. A cache entry under `cache.bin/<hash>` -- the orchestrator fetches and execs without recompiling on a hit. Hash is over the resolved sparks set + pipeline source.
2. A fresh compile from the working tree -- happens on cache miss, then publishes the result back to `cache.bin/<hash>` for next time.

Because compiled binaries live in the cache backend, the existing backend selection covers them. In CI you point `cache` at an s3 bucket and every run skips compilation if a previous build already populated `bin/<hash>`. Locally you point it at the filesystem and rebuild only when source changes.

An optional `cache.binaries` subspace isolates them for teams that want a shared binary cache while keeping local cache on disk:

```yaml
profiles:
  shared-team:
    cache:
      type: filesystem
      path: ~/.cache/sparkwing
      binaries:
        type: s3
        bucket: sparkwing-binaries
        prefix: "${PIPELINE_NAME}/"
```

### Per-pipeline backend override

For the case where one pipeline needs a different destination (prod runs write logs to a prod-only bucket for audit), declare a project profile and point the pipeline at it with `profile:`:

```yaml
# .sparkwing/sparkwing.yaml
profiles:
  prod-audit:
    secrets: { type: env }
    state:   { type: postgres, url_source: prod_state_db }
    cache:   { type: s3, bucket: prod, prefix: cache }
    logs:    { type: s3, bucket: prod-audit-logs, prefix: "${RUN_ID}/" }
pipelines:
  - name: release-prod
    entrypoint: Release
    profile: prod-audit
```

## Writing adapters for genuinely-different code paths

When data alone doesn't cover the difference -- kubectl vs client-go, SSO browser flow vs IRSA -- write a one-per-concern adapter that branches on a typed signal in one place. Pipeline business logic stays clean; the branching lives in the adapter.

### AWS auth -- usually data-only, no branching needed

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

### Kubernetes client -- genuinely two code paths

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

The branching still exists -- but:

1. **One location.** `mykube/client.go`. Not sprinkled across 12 job bodies.
2. **Typed signal.** `r.HasLabel("local")` reads a label set on a typed `Runner` value the orchestrator installed at dispatch. Not `os.Getenv("KUBERNETES_SERVICE_HOST")`.
3. **Driven by the declared topology.** The signal reflects which runner the scheduler picked, not heuristics about the environment. Running the same pipeline inside docker on your laptop won't accidentally trip the "remote" path.
4. **Testable.** Construct a context with `sparkwing.WithRunner(ctx, Runner{Labels: []string{"kubernetes"}})` and exercise the in-cluster branch from your laptop.
5. **Pipeline business logic stays clean.** `Run` reads as the business intent; the runtime decision is one level down.

### Preflight checks -- see Job-level selection

Preflight work that's only meaningful in some runner environments lives as its own job with `WhenRunner`. The DAG carries the eligibility condition; no branching needed.

## Triggers do not force runtime

Triggers -- `manual`, `push`, `schedule`, `webhook`, `pre_commit`, `pre_push` -- register the pipeline at whichever controller holds them. Where the trigger fires has no direct bearing on which runner runs the work. A webhook can fire on a laptop's local controller (via ngrok) and dispatch its work to cloud-linux; a scheduled run can fire on the shared controller and dispatch work to the local runner of a registered worker.

The only coupling is practical: scheduled triggers require a controller with scheduling enabled. That fact belongs in helptext, not in a hardwired runtime constraint.

## CLI surface

`sparkwing run` always executes on the machine you invoke it from; `--profile` selects the storage/auth surfaces, and remote dispatch is `sparkwing pipeline trigger --profile <name>`.

```bash
# Simple cases default cleanly: laptop CLI + default target + default runner.
sparkwing run lint
sparkwing run release --target dev

# Record state against a shared profile's backends.
sparkwing run release --profile shared --target staging

# Hand execution to a controller.
sparkwing pipeline trigger release --profile prod --target prod

# Introspect resolved plan + runner choices + secrets before pressing go.
sparkwing run release --target staging --sw-dry-run
sparkwing run release config --target staging
```

Autocomplete:

- `sparkwing run <TAB>` -- pipelines whose `requires:` intersects the current profile's `default_runner` (or all, if no default).
- `sparkwing run <pipeline> --target <TAB>` -- the target values the pipeline's arg schema accepts.
- `sparkwing run <pipeline> --profile <TAB>` -- profiles whose `default_runner` is in the pipeline's allowed set.

## Test scenarios

Each scenario is a concrete pipeline shape the design must support, with the constraint it tests.

| Pipeline | What it tests |
|---|---|
| `release` (multi-target with prod-from-remote-only) | Runner narrowing via `requires:`; approvals on prod; typed args per target |
| `release-pi` (local-only, local secrets source) | Profile-bound secrets source; static-runner pinning by unique label |
| `lint` (no target, runs anywhere) | Pipelines with no target arg ignore `--target`; CLI defaults pick cleanly |
| `migrate-db` (remote-only, prod approval) | `requires:` excludes local; the approval gate fires before any job dispatches |
| `investigate-prod` (read-only against prod) | Same target as `release` but different secret names; vault returns different values |
| `webhook-deploy` (target from payload) | Webhook trigger picks the target value from payload; rejected when the payload target is outside the pipeline's arg schema |
| `revert-deploy --emergency` (orthogonal modifier) | CLI flag bypasses approval gate; run logs an "emergency override" record |
| `train-model` (GPU pool) | `Requires("cloud-gpu")` routes to the Karpenter A100 pool; pod scheduled with correct tolerations/resources |
| `report-weekly` (scheduled) | Schedule registered on shared controller fires; run dispatches to the controller's `default_runner` |
| `scaffold-pipeline` (local-only, no target) | Local-only, no target arg, no secrets source to resolve |
| `build-windows` inside `release` (step-level offload) | Local CLI orchestrates; the `build-windows` job alone routes to `cloud-windows` while peers stay local |
| `preflight-sso` with `WhenRunner("local")` | Runs and may fail fast when on local runner; silently skipped on cloud-linux; `Needs` treats skip as satisfied |
| `release --target pi` from laptop | Orchestration on laptop; secrets from the laptop profile's source; deploy job runs on local runner |
| `release --target staging` from laptop | Orchestration on laptop; secrets from team vault (remote fetch); deploy job runs on cloud-linux |
| CI smoke run | A CI profile puts cache/logs/state in s3; compiled binary fetched from `cache.bin/<hash>` |
| Cluster-mode run (controller dispatch) | A controller profile routes logs and cache via the controller; runner pod materialized from runner `spec.nodeSelector` |
| Specific-machine pinning | Static runner declared with a unique label (`laptop=korey`); `Requires("laptop=korey")` only schedules on that machine; other runners ignored |
| Karpenter pool swap | A100 pool replaced with H100 by editing one runner entry; no pipeline code changes; next run schedules onto new node type |
| Heterogeneous fan-out | `JobFanOutDynamic` produces Workables that declare their own `Requires()`; each instance routes independently |
| Group-level requirement propagation | `GroupJobs(...).Requires("cloud-linux")` applies to all members; `Prefers` and `WhenRunner` propagate the same way |
| Adapter-driven kubectl/client-go branch | `mykube.New(ctx)` reads `sparkwing.Runner(ctx).HasLabel("local")` and returns the right implementation; pipeline body unchanged |

## What this replaces

**`RuntimeConfig.IsLocal` and the `SPARKWING_HOST` / `KUBERNETES_SERVICE_HOST` env-var detection.** `RuntimeConfig` is trimmed in place: `WorkDir` and `Git` remain. `IsLocal`, `RunID`, `NodeID`, env-var-driven mode detection are removed. `Debug` moves to a free function `sparkwing.Debug()`. Pipeline code reads typed config, calls `sparkwing.Secret/Config`, declares runner constraints, and inspects `sparkwing.Runner(ctx)` only inside one-per-concern adapters.

**The `Venue` enum** (`VenueEither`, `VenueLocalOnly`, `VenueClusterOnly`). Subsumed by the pipeline-level `requires:` allowlist plus the per-job verbs. The `Venue() sparkwing.Venue` optional method on pipeline values is removed.

**`TriggerInfo.Env`** (the untyped string map carried on `RunContext.Trigger`). A trigger spec declares only what it matches on (`branches:`, `paths:`); it carries no values of its own. Static per-project values live in the pipeline's `args:` map and reach job code through the typed `Inputs` struct, not through `rc.TriggerEnv("DEPLOY_ENV")`. The `TriggerInfo.Env` field and the `RunContext.TriggerEnv` accessor are removed.

**The `secrets:` list on `Pipeline`.** Now the fail-fast typed `Secrets()` provider; a pipeline declares a struct, and the orchestrator resolves every required field at run start.

**`SPARKWING_LOG_STORE` and `SPARKWING_ARTIFACT_STORE`** env vars. Replaced by per-profile backend surfaces. The env vars no longer influence backend selection.

## Out of scope for v1

- **Cross-controller dispatch.** A run uses one orchestration controller; every runner it touches must be reachable from that controller.
- **Custom backend types beyond the listed cloud providers.** Type-discriminated structure is in place; concrete backends land as demand surfaces.
- **Per-secret source override.** A run picks one secrets source, from its resolved profile.
- **Runtime runner advertisement updates.** Runners are loaded at process start; hot-reload of runner declarations is not supported in v1.

## What shipped

The parts of this design that are the current model:

- **Label-matched runner selection.** A pipeline states its allowlist with `requires:` in `.sparkwing/sparkwing.yaml`; jobs narrow it with `Requires`, `Prefers`, and `WhenRunner` on `*JobNode` and `*JobGroup` (groups delegate to every member). The reserved label `local` pins a pipeline to the in-process runner.
- **Workable-declared constraints.** `Requires() []string` and `Prefers() []string` are optional interfaces (`sparkwing/workable_labels.go`); the orchestrator reads them when it wraps a Workable into a `*JobNode`, so a fan-out instance can carry its own contract.
- **Typed runner inspection.** `sparkwing.Runner(ctx)` returns the `*RunnerInfo` the orchestrator installed at dispatch, with `HasLabel` for adapter branching. It is nil outside a dispatched job.
- **A trimmed `RuntimeConfig`.** `WorkDir` and `Git` remain. `IsLocal`, `RunID`, `NodeID`, the `Debug` field, and every env-var venue detection are gone; `sparkwing.DebugEnabled()` reads the `SPARKWING_DEBUG` flag, and IDs live on `RunContext` and the per-job context. The `Venue` enum is deleted.
- **Profile-scoped backends and secrets.** A pipeline's `profile:` (or `--profile`, or the project's `defaults.profile`) picks one profile, and that profile's `state:` / `cache:` / `logs:` / `secrets:` surfaces apply wholesale.
- **Fail-fast typed secrets.** `Secrets() any` plus `sparkwing.PipelineSecrets[T](ctx)`; required fields resolve before the first job dispatches.

What the design sketched but the shipped model does **not** have: the `targets:` map and its per-target `runners:` / `values:` / `source:` / `backend:` overrides, per-pipeline `runners:` and `values:` keys, the typed `Config()` provider and `sparkwing.PipelineConfig[T]`, the standalone `runners.yaml` / `sources.yaml` / `backends.yaml` files, and `values:` blocks on trigger specs. The v0.5.0 config flatten and the v0.6 Config removal took them out; the strict parser rejects each one by name.
