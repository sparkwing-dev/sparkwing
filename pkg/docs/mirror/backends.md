# Storage backends

Backends are configured per profile, not in a separate file. A profile
declares four persistence surfaces plus how to reach a controller:

- **state** -- run records, plan snapshots, status
- **cache** -- content-addressed artifacts and compiled pipeline binaries
- **logs** -- per-job log streams
- **secrets** -- where `sparkwing.Secret` values resolve from

A profile describes where a command reads and writes and what auth it
needs to get there. `sparkwing pipeline trigger <pipeline> --profile
NAME` runs a pipeline on the profile's controller, and read verbs such as
`sparkwing runs list --profile NAME` accept any profile. `sparkwing run`
executes on this machine and, without `--profile`, uses its local
surfaces. It accepts `--profile NAME` only for a profile with no
controller whose state is `s3` or `postgres`, and then records state,
logs, and cache to that profile's backends; this is a temporary exception
for teams that coordinate through shared storage, slated for removal.
Laptop
profiles live in the `profiles` section of `~/.config/sparkwing/config.yaml`
(see [Machine settings](machine-config.md)); project profiles
in `.sparkwing/sparkwing.yaml` (see [config-reference.md](config-reference.md)).

```yaml
# ~/.config/sparkwing/config.yaml
profiles:
  laptop:
    state: { type: sqlite }
    cache: { type: filesystem, path: ~/.cache/sparkwing }
    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }

  shared-team:
    state: { type: s3, bucket: team, prefix: state }
    cache: { type: s3, bucket: team, prefix: cache }
    logs:  { type: s3, bucket: team, prefix: logs }

  prod:
    controller: { url: https://api.example.dev, token: swu_xxx }
    # state/cache/logs are implied by the controller; reads/writes go through it.
```

Select a profile with `--profile NAME`; it applies wholesale. Without
`--profile`, a command uses the built-in local (sqlite + filesystem)
defaults; no environment variable or project default selects a profile. `sparkwing configure profiles show` prints which profile resolved and why.

## Backend types

| Surface | Types | Use |
| --- | --- | --- |
| `state` | `sqlite`, `postgres`, `s3`, `controller` | Run records, plan snapshots, status |
| `cache` | `filesystem`, `s3`, `controller` | Content-addressed artifact and compiled-binary store |
| `logs`  | `filesystem`, `s3`, `controller`, `stdout` | Per-job log stream persistence |

### Object-store log batching

An object store charges per request, so the `s3` logs surface buffers
each node's lines and writes one object per flush rather than one per
line. Four optional keys tune it; every one defaults to a value that
suits a chatty CI node, and a profile that names none behaves the same
as one that names the defaults.

| Key | Default | Meaning |
| --- | --- | --- |
| `batch_interval` | `2s` | Longest a buffered line waits before its object is written |
| `batch_bytes` | `262144` | Buffer size that triggers an early flush |
| `max_log_objects` | `2000` | Objects one node's log may cost |
| `max_log_bytes` | `67108864` | Bytes one node's log may hold |

```yaml
profiles:
  team:
    logs:
      type: s3
      bucket: my-team-sparkwing
      prefix: logs/
      batch_interval: 5s
      batch_bytes: 524288
```

A flush also lands when a reader asks for the node's log, and when the
node finishes. The finish flush runs before the node's status is
written, on the failing and cancelled paths as well as the succeeding
one, so a node whose status reads terminal has a complete log. Past
`max_log_objects` or `max_log_bytes` the surface drops further lines and
ends that node's log with one marker line counting them. A flush the
object store refuses loses its whole batch; those lines are counted into
the node's dropped-line total and reported as a `logs_drop` event.

The keys are valid only on an object-store logs surface. A `filesystem`
surface appends to an open file and a `controller` surface uploads
segments, so neither reads them, and a profile that sets one
anywhere else is refused at load with the key named. A negative value is
refused the same way.

Each state backend is one deployment shape. See
[Deployment modes](deployment-modes.md) for when to pick each:

- `sqlite` -- the local path, and a single-box controller; the default
  when no profile is selected.
- `s3` -- per-run NDJSON state on a shared bucket, written by
  `sparkwing run --profile NAME` (a temporary exception slated for
  removal). Cache reservation, approvals, and debug pauses coordinate
  over object-store CAS where the bucket enforces write preconditions.
  Where it does not, cache reservation degrades to last-write-wins,
  while approvals and debug pauses report not-supported and need
  Postgres. Pipeline triggers report not-supported here whatever the
  bucket does: the backend enqueues a trigger and has no path that
  claims one, so `sparkwing.RunAndAwait` refuses instead of waiting.
- `postgres` -- a self-hosted controller's database, or a database
  runners share through `sparkwing run --profile NAME` (the same
  temporary exception). Triggers, approvals, debug pauses all work.
- `controller` -- commands reach a hosted controller over HTTP,
  Sparkwing Cloud included. The controller owns the underlying database.
  `sparkwing run` refuses a controller profile; use `sparkwing pipeline
  trigger <pipeline> --profile NAME`.

Any other type fails at run start with an error naming the types its
surface accepts.

Local execution is process-per-node under every state backend. A node
body runs in its own process of the pipeline binary and reaches run
state through a controller the dispatcher mounts on loopback for the
run: the full controller when state is a local SQLite database, and the
node-facing subset of the same API over whatever else the profile named
-- object-store state included. Nothing local executes inside the
dispatcher's own process, so a bucket-backed CI run and a laptop run
behave the same way.

One measurement does not follow. The measured pipeline profiles that
size admission are folded from the local run store, so only `sqlite`
state feeds them; a bucket-backed run records its per-node metric
samples in the bucket but folds no profile and stores no exit
accounting. Run without `--profile` on the machine whose capacity you
want learned.

Required fields per type:

- `filesystem` -- `path`
- `s3` -- `bucket` (plus optional `prefix`)
- `postgres` -- exactly one of `url` or `url_source` (the
  latter names a secret in the resolved source)
- `controller` requires `controller: <profile-name>` or `url:`. A profile with a sibling `controller:` block inherits that profile name. A `logs` surface of type `controller` takes no `url:`: its node logs reach the controller as segments, so the profile's state must be on the same controller, and a url there is refused at load.
- `stdout`, `sqlite` -- no required fields

Recognized backend types that aren't implemented in the current
build surface a clear error at run start ("type X is recognized but
not implemented in this build") instead of silently falling back.

The fourth surface, `secrets`, names where `sparkwing.Secret` values
resolve from (this machine's local secret store or controller-stored); see
[security.md](security.md). A local run reads this machine's secret store
(`sparkwing secrets set`), unless it runs under an S3 or Postgres state
profile whose own `secrets:` surface names another source.

With a controller secrets surface, runs and coordinated nodes identify
the current run so the controller selects that pipeline's scoped values. The
controller still enforces the caller's permissions and, for ordinary readers,
a live claim on the run. Standalone resolver calls without a run binding retain
unscoped lookup behavior.

## Pinning a pipeline to a profile

`profile:` on a pipeline entry pins that pipeline to a controller
profile:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: release-prod
    entrypoint: Release
    profile: prod
```

A pinned pipeline runs only on that profile's controller.
`sparkwing run release-prod` refuses it (`pipeline release-prod is pinned
to profile prod; use sparkwing pipeline trigger release-prod --profile
prod`), and so do detached runs, crons, and local child pipelines.
`sparkwing pipeline trigger` requires `--profile` and refuses any profile
other than `prod`. `sparkwing pipeline describe`
reports the pin as `pinned_profile` in JSON and a `pinned:` line in text,
and `sparkwing pipeline plan` prints a `Pinned:` line.

The pin guards against sending a pipeline to the wrong place by mistake.
It is not a security boundary: the controller decides who may run a
pipeline and which secrets its runs see.

## Pipeline binary distribution

Compiled pipeline binaries live in the cache surface under `bin/<hash>`.
On a cache hit, the orchestrator fetches and execs without recompiling.
An optional `cache.binaries` sub-spec isolates binaries to a separate
destination:

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

A pipeline compile reads `bin/<hash>` from the sub-spec when one is
declared and from the cache surface otherwise. The publish command writes
that same destination when `--profile` names this profile, so what it
uploads is what a later run finds; its `--artifact-store` URL names a
destination outside any profile. Only one level is read: a `binaries` block
inside a `binaries` block is ignored.

## Migrating from `backends.yaml`

For the before/after of moving `backends.yaml` `defaults:` and
`environments:` into per-profile specs, see the
[v0.5.0 migration guide](migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
