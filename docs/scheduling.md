# Scheduling

How sparkwing decides which agent runs a job. The model is **deliberately
Kubernetes-shaped** so anyone who's seen `nodeSelector` /
`tolerations` / `nodeAffinity` will recognize it — but the YAML is
flatter and the controller fills in sane defaults.

## The model in one paragraph

Agents have **labels** (what they are) and **taints** (what they refuse
to run by default). Pipelines declare scheduling intent via a
**`runs_on`** block in `pipelines.yaml`. The controller's matcher
filters out agents the job is incompatible with, then picks the
best-scoring match. If nothing matches in time the job fails with a
queue-timeout error.

## Per-node `.Requires()` (Go SDK)

Alongside the pipeline-level `runs_on` block (below), individual nodes
can declare runner labels from Go code via the `.Requires()` modifier on
`*sparkwing.JobNode`:

```go
sw.Job(plan, "train", &Train{}).Requires("gpu")
sw.Job(plan, "package-arm", &Package{}).Requires("arch=arm64", "trusted")
```

**AND semantics.** A node declaring `.Requires("arm64", "laptop")` is
claimable only by a warm runner whose `--label` set contains BOTH
`arm64` and `laptop`. A runner advertising a superset (e.g. also
`trusted`) still matches. To express OR, author separate nodes. There
is no selector / expression syntax in v1 -- labels are compared as
literal equality strings.

**No matching runner.** If no connected runner advertises the required
labels, the node blocks indefinitely. The orchestrator logs a warning
once per minute naming the unclaimed label set. The escape hatch is
either: start a runner with `--label` matching, or remove the
`.Requires()` modifier and retry the run.

**Where labels come from.** Runners advertise labels at claim time via
the `wing runner --label` flag (repeatable), or the pod spec argv in
`k8s/sparkwing/pool/deployment.yaml`. The controller's claim SQL
filters candidate nodes whose `needs_labels` is a subset of the
runner's advertised set.

**Relationship to `pipelines.yaml` `runs_on`.** The YAML block is the
legacy trigger-level selector (see below); `.Requires()` is a node-level
modifier that writes `needs_labels` directly on the node row. Both can
coexist -- the YAML filters which agent picks up the TRIGGER, while
`.Requires()` filters which warm runner claims each NODE inside the run.

## Pipeline `runs_on`

```yaml
my-build:
  on:
    push:
      branches: [main]
  runs_on:
    require:                  # hard filter — must all match
      os: linux
      arch: amd64
    prefer:                   # soft preference — extra points per match
      zone: us-east
    tolerate:                 # which agent taints this job accepts
      - agent=local
      - gpu:NoSchedule
    queue_timeout: 15m        # default 10m; "forever" disables
```

| Field | Maps to k8s | Meaning |
|---|---|---|
| `require` | `nodeSelector` / required nodeAffinity | Every key/value must match the agent's labels (or `name`/`type` pseudo-labels). If any miss, the job is **not eligible** for that agent. |
| `prefer` | preferred nodeAffinity | Soft scoring — each matching key adds +10 to that (job, agent) pair. Doesn't filter, just sorts. |
| `tolerate` | `tolerations` | List of tolerations. Every `NoSchedule` taint on the agent must be tolerated, or the job is rejected. `PreferNoSchedule` taints lower the score by 1 instead of blocking. |
| `queue_timeout` | (no k8s analogue) | How long the job may sit pending before the controller fails it. Empty → 10m default. `forever` or `never` disables. |

### Toleration shorthand

Tolerations accept four forms — pick whichever reads cleanest:

```yaml
tolerate:
  - agent=local                                # Equal, NoSchedule (most common)
  - agent=local:PreferNoSchedule               # Equal, explicit effect
  - gpu                                        # Exists, NoSchedule
  - { key: zone, value: us-east, operator: Equal, effect: NoSchedule }
```

The first three are sugar for the long form. `Operator: Exists` matches
any value with the named key — useful when a taint exists for *any*
value of, say, `gpu`.

## Agent labels and taints

Agents register themselves with the controller on every poll, sending
their labels and taints in the URL.

### From `~/.config/sparkwing/config.yaml`

```yaml
default_profile: local
agent_name: dev-laptop
profiles:
  local:
    controller: http://localhost:9001
    agent_type: local
    agent_labels:
      os: darwin
      arch: arm64
      zone: home
    agent_taints: agent=local:NoSchedule    # optional override
```

### From environment variables (typical for k8s deployments + laptop workers)

```bash
SPARKWING_AGENT_TYPE=warm-runner
SPARKWING_AGENT_LABELS=cluster=prod,arch=arm64
SPARKWING_AGENT_TAINTS=spot:PreferNoSchedule
```

These are read by both `sparkwing-runner` (the cluster-mode daemon
running inside the warm pool) and `sparkwing cluster worker` (the
laptop-side queue drainer).

### Running a laptop worker

```bash
SPARKWING_AGENT_TAINTS=agent=local:NoSchedule \
  sparkwing cluster worker --on prod
```

(`--on prod` pulls controller URL + token from the prod profile;
see `docs/auth.md` on registering profiles.)

### Pseudo-labels: `name` and `type`

`require: { type: warm-runner }` and `require: { name: lap-7 }` work
even though the agent didn't explicitly declare `type` and `name` as
labels. The matcher resolves them from the agent identity.

## The `local` taint (laptop default)

When you run `sparkwing cluster worker` with `agent_type: local` (the
default), the agent **automatically advertises a `local:NoSchedule`
taint**. This means a stray webhook push doesn't end up running on
someone's laptop -- pipelines must explicitly opt in:

```yaml
my-laptop-pipeline:
  runs_on:
    tolerate: [local]
```

You can override the default by setting `SPARKWING_AGENT_TAINTS` in the
worker's environment (or `agent_taints` in the profile). Set it to an
empty string to advertise no taints at all (and accept any job).

## Direct invocations (`wing`) bypass taints

When you run a pipeline directly with `wing` (without `--on <cluster>`),
sparkwing marks the job as **`Direct`**. Direct jobs:

- Skip the taint check entirely — you've already chosen the agent (your
  laptop, this terminal), so untolerated `NoSchedule` taints don't repel.
- Skip the queue-timeout sweeper — you'll cancel manually if needed.

This is the key distinction the user cares about: **webhook → controller
→ scheduling matters; `wing build-deploy` → just run it here**.

`wing build-deploy --on prod` is *not* direct: you've explicitly chosen
to dispatch to a remote controller, which then schedules normally.

`require` and `prefer` are still respected for `wing` invocations —
nothing forces a `linux` pipeline to compile on a Mac.

## Scoring (how ties are broken)

Each (job, agent) pair earns a score:

| Rule | Delta |
|---|---|
| Each matching `runs_on.prefer` key | **+10** |
| Legacy `prefer` selector match | **+10** |
| Legacy `prefer` selector mismatch (anti-affinity) | **−1** |
| Each untolerated `PreferNoSchedule` taint | **−1** |

The matcher picks the highest-scoring eligible job. Ties are broken by
**FIFO** (oldest `created_at` wins) so nothing starves indefinitely.

## Queue timeout

Pending jobs that no agent will claim are eventually failed. Defaults
and overrides:

| `queue_timeout` value | Effect |
|---|---|
| (unset) | 10 minutes (`DefaultQueueTimeout`) |
| `30s`, `5m`, `1h30m` | parsed via `time.ParseDuration` |
| `forever` / `never` | Never expires — job sits pending until claimed or cancelled |

The controller's cleanup loop runs once a minute. When a queue-timeout
fires, the job's logs explain *why* nothing claimed it (no matching
labels, no toleration for taint X, etc.).

Direct (`wing`) jobs are exempt — see above.

## Worked examples

### Run only on the warm runner pool

```yaml
deploy-prod:
  runs_on:
    require:
      type: warm-runner       # k8s warm pool only
```

### Prefer ARM but accept anything

```yaml
build-image:
  runs_on:
    prefer:
      arch: arm64
```

If two ARM warm runners and one AMD warm runner are all idle, ARM wins
by score. If only AMD is idle, the job runs there.

### A laptop-specific pipeline

```yaml
seed-local-db:
  runs_on:
    require:
      type: local             # only laptops
    tolerate:
      - agent=local           # accept the default local taint
    queue_timeout: forever    # might be offline overnight
```

### Spot-aware build with degraded preference

```yaml
nightly-rebuild:
  runs_on:
    tolerate:
      - spot:PreferNoSchedule
    prefer:
      zone: us-east
```

`spot:PreferNoSchedule` tolerated → spot agents are eligible. The
`-1` penalty per untolerated `PreferNoSchedule` doesn't apply because
it *is* tolerated. Among eligible agents, `us-east` ones score
higher.

## Backward compatibility

The old single-string `prefer:` and `require:` selectors on `Job` are
still parsed and AND-combined with `runs_on`. Old DB rows and old
`pipelines.yaml` files keep working without migration.

```yaml
# Both styles in one job — both must match.
old-and-new:
  prefer: type:listener     # legacy
  runs_on:
    require: { os: linux }  # new
```
