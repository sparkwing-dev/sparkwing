# Migrating to the next release

## Recreate the store for schema baseline 28

v0.68.3 ships SQLite and PostgreSQL schema baseline 28 as a store reset. There
is no forward migration. Stores at another baseline refuse startup and remain
unchanged. Before 1.0, schema changes may require moving the local store aside
or recreating a server database. Forward migrations start after launch.

For a local store:

1. Stop the local daemon with `sparkwing daemon stop`, then stop any standalone
   controllers and runners using the store.
2. Back up the entire Sparkwing home, including `state.db` and any `state.db-wal`
   and `state.db-shm` files. Use `~/.sparkwing` unless `SPARKWING_HOME` names
   another directory. With the processes stopped, run:

   ```bash
   store_home="${SPARKWING_HOME:-$HOME/.sparkwing}"
   backup_home="${store_home}.before-v0.68.3-$(date +%Y%m%d%H%M%S)"
   cp -a "$store_home" "$backup_home"
   mv "$store_home" "${backup_home}.moved-aside"
   ```

3. Install v0.68.3 and run `sparkwing runs list`. The local controller starts
   on demand and creates a fresh store in the original home. Sign in and restore
   configuration as needed. Do not copy the old database or its WAL/SHM files
   into the new home.

For a server store:

1. Stop every controller and runner using the database, including staging or
   production replicas.
2. Back up the database and the controller's credential directory. Keep the
   backup with the binary that wrote it. For PostgreSQL, use `pg_dump --format=custom`
   with the existing connection settings and save the dump outside the database.
3. Create a new empty PostgreSQL database with `createdb` using the server's
   database owner and connection settings. Point the controller's store URL at
   that database. Alternatively, after verifying the backup, drop and recreate
   the disposable database. For a server using SQLite, back up and move its
   database and any WAL/SHM files aside before restarting.
4. Start the v0.68.3 controller against the empty store. Recreate teams, secrets,
   credentials, repository registrations and schedules, then enroll runners
   again. Restore configuration without restoring the old schema.

Staging and production require the same reset. See
[backup and restore](../backup-restore.md#schema-compatibility).

The baseline removes unused session, capability, node, trigger and storage
columns and the insert-only team counter table. It requires complete financial
decisions, nonempty credit grant references, positive award generations and
one open billing interval per award.

## Sealed execution trust

Intake now seals repository proof and verified default-branch eligibility.
Claim issuance seals host kind and cache scope. These facts govern every trust
consumer and survive delegation unchanged. The schema baseline is 28; see the store
recreation section above.

Personal machines and here hosts cannot claim repository-proven runs, including
run-now and burst `machines` work. Use a shared runner or Sparkwing Cloud.
Repository-scoped runners reject asserted uploads even when the remote name
matches `--repos`. Omit `--repos` when enrolling a personal runner so it can
claim its owner's asserted work. Shared enrollment still requires a repository
scope.

Job source credentials require both repository proof and a successful intake
GitHub compare check. Host-wide credentials no longer bypass proof. Uploaded,
local and unproven remote code receive no team credential. A historical commit
or locked schedule can qualify after App source selection and a compare result
of `ahead` or `identical` against a repository branch or tag for both source and
execution commits when they differ. Merged PR runs compare against their base
branch. If the intake compare cannot reach GitHub, times out, receives a 5xx or
supplies unreadable JSON or an unknown status, webhook, native trigger and
schedule intake admit the run as asserted, without
repository proof, job credentials, proven-repository secrets or OIDC claims.
Run status explains the failed verification. Definite negative compare answers
retain their existing behavior. Verified cache fallbacks require sealed
repository proof. Such a historical commit carries no resolved-head OIDC `ref`
or `sha`.

Pipeline references now select verified default-branch runs and exclude
publication. To accept other producers, declare `sw.FromAnyRun()` in Go or
`{fromAnyRun: true}` in TypeScript. The direct output route also requires that
declaration. Existing unverified outputs no longer satisfy the default lookup.

## Capacity profiles report measured data

Sizing and capacity responses drop stored resource pins, demand floors,
queue-wait percentiles and contention counters. Update clients that decode
those fields or the pin/floor estimate sources. The console and CLI continue
to show measured resource distributions and predecessor estimates. Local daemon
admission resource pins retain their existing behavior.

The OpenAPI schemas `components.schemas.internal.controllerwire.JobSizing`
and `components.schemas.internal.controllerwire.SizingMeasurements` remove
`pinned_cores`, `pinned_memory_bytes`, `floor_cores`, `floor_memory_bytes`,
`wait_p50_ns`, `wait_p99_ns`, `wait_sample_count` and `contended_count`.
`components.schemas.internal.controllerwire.SizingDetail` removes `drift`.
Remove dependencies on these fields before upgrading.

## Teams have no member cap

Team membership is unlimited for free, funded and payment-held teams. Remove
`free_team_members` from operator signup requests and response handling. The
invitation creation response drops `warning`, and acceptance no longer returns
`team_full`. Keep handling `invitation_not_found` and `invitation_closed`.
The inviter must still hold team administration, and the accepting account
must own the invited address.

The unreleased schema baseline remains 28 and removes
`signup_gate.free_team_members`. Recreate pre-release stores with the updated
baseline using the instructions above.

## Shared Cloud capacity

Replace per-team NodePools with one shared NodePool per requested vCPU class and
deployment. Retire the old pools only after their runs finish and their nodes
are empty. Keep their permanent used-team and used-run labels until Karpenter
removes the nodes. Never relabel a used node into a shared pool.

Shared templates set `node-restriction.kubernetes.io/sparkwing-cloud-class` to
the requested vCPU class, `sparkwing.dev/launcher-namespace` to the Cloud Job
namespace and `sparkwing.dev/warm-namespace` to the trusted warm Deployment
namespace. Warm placeholders must move to their own namespace, separate from
both the release and customer Job namespaces. The chart creates
`<release namespace>-cloud-warm` by default. Set `launcher.warmNamespace` to
override it, and update the pool's warm-namespace label to match. Remove the old
release-namespace warm-capacity Role and RoleBinding. The new Role grants Pod
get/delete and ReplicaSet get only in the warm namespace. Placeholders must be
volume-free and owned through a ReplicaSet by `sparkwing-cloud-warm-<vCPU>`.

Set `launcher.nodeClassName` (or `sparkwing-runner launch --node-class`) to the
hardened EC2NodeClass name. Both default to `sparkwing-jobs`. Pools whose
nodeClassRef name, group or kind differs are refused. They use
`sparkwing.dev/dedicated=true:NoSchedule` instead of the
team taint. Pool templates carry no protected team label. The launcher binds
that exact label and permanent team/run fences at first reservation.

Use the [shared pool example](../../charts/sparkwing-full/examples/shared-nodepool.yaml).
The [infrastructure proposal](../../design/rewrite/proposals/shared-cloud-capacity.md)
contains exact production and staging manifest replacements. Apply the launcher,
admission and pool changes together. Operator setting `cloud_upsize_enabled`
defaults to true and controls use of an idle warm node one class larger. Billing
and CPU/memory limits retain the requested class. No deployment is performed by
this repository change.

Warm capacity defaults to Off for every class, including after the baseline 28
store reset above. Install KEDA separately, then enable `warmCapacity.enabled`
with `controller.store=postgres`, the Cloud job namespace and the read-only
connection Secret. KEDA scales `sparkwing-cloud-warm-<vCPU>` in the dedicated warm namespace.
Its ScaledObject, TriggerAuthentication and reader Secret live there too.
The controller's database owner creates `warm_pool_desired`; the scaler role
needs SELECT only on that view. Placeholder containers receive no credentials.

Open **Operator > Warm capacity**, or run:

```bash
sparkwing operator warm-capacity show --profile deployment
sparkwing operator warm-capacity set --class sparkwing-2cpu --mode manual --count 2 --profile deployment
sparkwing operator warm-capacity set --class sparkwing-2cpu --mode auto --min 0 --max 4 --start-time-target 10s --profile deployment
sparkwing operator warm-capacity set --class sparkwing-2cpu --mode off --profile deployment
```

`set` replaces the selected policy. Omitted counts become zero and the target
becomes 10s. Auto takes the larger of half the cold-start waits in 30 minutes,
rounded up, and current warm nodes plus one when the last hour's median start
exceeds the target. It clamps that result to the configured min/max. No demand
samples gives the minimum. Counts must fit Kubernetes replicas (0..2147483647).
The view owns the replica target; infrastructure NodePool budgets govern spend.

Start percentiles group jobs by requested class, including upsized jobs. No start
samples appears as an empty metric. Cold starts include jobs still waiting for a
fresh node. Upsize counts use execution starts in both windows. Signups count
accounts created, and active teams count teams that submitted runs, over 24h/7d.
The launcher reports ready, unbound nodes each poll.
Samples expire after 120 seconds so a stopped launcher cannot report stale warmth.
