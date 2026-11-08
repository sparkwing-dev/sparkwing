# Backup, restore, and upgrade

This runbook covers an operator-managed controller using SQLite or PostgreSQL.
Keep a database backup, its matching controller image or binary, and the keys
needed to open it. Rehearse recovery on an isolated controller before upgrading
the live one. See [self-hosting](self-hosting.md) for deployment requirements.

## Recovery inventory

Record the controller version and image digest, startup arguments, database
schema and baseline digest, backup time in UTC, and the object-store location.
Read the schema without opening it with a different Sparkwing binary:

```sql
SELECT version, baseline_digest FROM sparkwing_schema_version;
```

Back up the following together. Store the manifest beside the backup, and keep
credentials in encrypted storage with access restricted to recovery operators.

| Item | Recovery requirement |
| --- | --- |
| Database | SQLite `state.db` and any WAL sidecars, or a PostgreSQL backup |
| Secret encryption | `secrets.key` beside SQLite, or the mounted `secrets-key` and `secrets-key.previous` credentials. Retain retired keys for older backups |
| Credential epoch | The **latest** external `credential-epoch`, including its separate persistent volume when `--credential-epoch-dir` is set. Never overwrite it with an older backup's epoch |
| Deployment credentials | The credential directory, license, OIDC keys, GitHub App keys and webhook secrets, OAuth secrets, database credentials, and other configured integrations |
| Deployment configuration | Service definitions or Helm values, image digests, controller URL, secret references, and runner configuration and credentials on runner hosts |
| Objects | Files under the controller home, or the external object store holding logs, artifacts, source bundles and other run data. Database backups do not copy external objects |
| Audit records | An external audit stream covering changes since the restore point, so revoked access can be revoked again |

Supported upgrades retain configured data. A normal restore retains records
present at the backup's timestamp. A reset to an empty database preserves none
of it by itself:

| Configured data | What must survive an upgrade or planned reset |
| --- | --- |
| Secrets | Encrypted values, scope and masking settings, plus their encryption keys |
| Teams | Team identity, accounts, memberships and roles |
| Tokens | Token records, grants and revocations, plus the live credential epoch |
| Runners | Registrations, capability records, repository scope and runner-host credentials. Liveness returns when runners reconnect |
| GitHub App installs | Installation and repository bindings, plus App credentials outside the database |
| Schedules | Definitions, subscriptions, source bindings and consent. Restore preparation holds execution until acknowledgement |

Run history, logs and caches may be discarded in an approved reset. Review
billing records separately before changing or discarding them. There is no
general configuration-only export/import command: use the supported in-place
upgrade or recover the complete database. If a release requires a reset, stop
until its migration procedure provides a verified path for every configured
item above. Moving a configured store aside and starting empty is not recovery.

## Backup

Choose retention and backup frequency from the amount of data you can afford
to lose. Copy backups off the controller host and test that recovery operators
can decrypt them. Record the last successful restore drill. Automatic schema
backups described under [schema compatibility](#schema-compatibility) keep only
two local copies and do not replace this policy.

### SQLite

Pause inbound triggers and stop runners from taking new work. Let active runs
finish, then stop every process that opens this store, including the controller
and any local CLI or daemon sharing it. Disable automatic restarts for the
maintenance window. For a local CLI controller, use `sparkwing daemon stop`.
For a standalone controller, stop its service or scale its deployment to zero.

The following Linux example copies a stopped controller home. Substitute absolute
paths for your deployment, and run it as the controller's OS user:

```bash
set -eu
umask 077
store_home=/srv/sparkwing
backup=/srv/backups/sparkwing/pre-upgrade
test -f "$store_home/state.db"
mkdir -p /srv/backups/sparkwing
mkdir "$backup"
cp -a "$store_home" "$backup/controller"
sqlite3 "$backup/controller/state.db" 'PRAGMA integrity_check;'
sqlite3 "$backup/controller/state.db" \
  'SELECT version, baseline_digest FROM sparkwing_schema_version;'
```

The integrity check must print `ok`. Copy external credential and epoch
directories into the protected backup too. Follow symlinks when copying mounted
Kubernetes Secret projections, so the backup contains the credential bytes
rather than links to a mount that will disappear. Include `state.db-wal` and
`state.db-shm` if present. Copying only a live `state.db` can lose committed
transactions. Resume the original service after the copy is complete.

SQLite restores a captured snapshot. Arbitrary point-in-time recovery between
snapshots requires a separate continuous backup system. Pick the last verified
snapshot before the incident and record the data-loss interval.

### PostgreSQL

Use a PostgreSQL client at least as new as the server. Configure `pg_service.conf`
and a private password file for `service=sparkwing` and
`service=sparkwing-recovery`, pointing at the source and a new empty recovery
database respectively. This keeps passwords out of command arguments and logs.

Pause admissions, drain active runs and stop all controllers before the backup
when it will be used for an upgrade rehearsal or binary rollback. A normal
`pg_dump` also takes a consistent database snapshot while the server is live,
but external files and objects need their own coordinated recovery point.

```bash
set -eu
umask 077
backup=/srv/backups/sparkwing/pre-upgrade
mkdir -p /srv/backups/sparkwing
mkdir "$backup"
pg_dump --format=custom --no-owner --no-privileges \
  --file="$backup/controller.dump" 'service=sparkwing'
pg_restore --list "$backup/controller.dump" > "$backup/contents.txt"
psql 'service=sparkwing' -X -v ON_ERROR_STOP=1 -c \
  'SELECT version, baseline_digest FROM sparkwing_schema_version;'
```

Back up credentials and external objects from the inventory separately.
`--no-owner --no-privileges` lets the recovery role own the restored objects.
Record and recreate the deployment's database roles and grants separately.

A dump restores the snapshot taken by that dump. For arbitrary PostgreSQL PITR,
configure and verify a base backup and continuous WAL archiving **before** the
incident. Recover to a new database server with a UTC recovery target, stop
replay at that target and promote only after checking the recovered data.
`pg_dump` is not a base backup for WAL replay. Follow
[PostgreSQL's PITR procedure](https://www.postgresql.org/docs/current/continuous-archiving.html)
or your database provider's recovery procedure. Sparkwing's restore preparation
still applies after database recovery.

## Rehearse the migration on a copy

1. Restore the backup into a private home or a separate PostgreSQL database.
   Use a private credential directory, keep the original encryption key and
   latest epoch, and change only the recovery copy's database connection. Run
   `restore prepare` with the matching old binary before starting the copy.
2. Block outbound webhooks, GitHub writes, payment callbacks and runner access
   from the rehearsal. Bind the controller to loopback. A restored schedule
   must not dispatch real work. Do not point a rehearsal at production object
   storage with write access.
3. Start the matching old controller once, verify the configured data inventory,
   then stop it. Record the schema and baseline digest.
4. Start the target controller against the copy. Startup performs a supported
   migration before serving requests. Read the startup log, schema and digest.
   On SQLite, check that the pre-upgrade `.bak` exists and passes integrity
   checking. A refusal is a stop condition, not permission to edit the version
   or digest in SQL.
5. Run the [acceptance checks](#acceptance-checks). Stop the copy and restore the
   pre-upgrade backup again with its matching binary. Repeat acceptance. Record
   the versions, restore point, elapsed recovery time and results.

An isolated rehearsal verifies the database and keys. Recheck the live URL,
external integrations and runner connectivity during the actual maintenance.

## Upgrade order and rollback

1. Read the release's migration notes. Record the old and target versions of the
   controller, runners, launcher and CLI. Keep the old images and take a verified
   backup. Rehearse the upgrade and restore before the maintenance window.
2. Pause triggers, drain runs, stop runners and stop every controller sharing the
   database. Prevent a service manager, Helm or deployment automation from
   automatically restarting an older controller on the migrated database.
3. Upgrade the controller first. Start one instance to migrate the database,
   check health, then start any remaining instances at that same version.
   **The first committed schema migration is the point where an image-only
   rollback can become unsafe.**
4. Upgrade runner and launcher binaries/images to the release's matching set,
   retaining their credentials, names and repository scopes. Reconnect one
   runner and complete a smoke run before admitting the rest.
5. Upgrade the operator and user CLIs, verify their saved controller profiles,
   and complete a CLI-submitted smoke run. Repository SDK pins are separate:
   follow the release notes before running `sparkwing pipeline upgrade`.
6. Complete acceptance checks, resume triggers and watch the first scheduled
   run and webhook delivery. Retain the backup until the recovery window ends.

Never roll a controller back below the database's schema or to a different
baseline digest at the same version. If startup says the database is newer,
roll forward to a binary that supports it. Do not repeatedly restart the old
binary, change the schema version by hand, or delete configured state.

If no schema changed, a prior image is an option only after checking its schema
and wire contracts. If a schema changed, prefer a repaired forward release. An
older binary requires a complete matching pre-upgrade backup restored to a
separate store, the original keys and the latest epoch. Stop all writers before
switching stores and accept the loss of changes since that backup. Reconcile
revocations and billing before reopening access. Never use `kubectl rollout
undo` or `helm rollback` without checking this database boundary.

## Restore

1. Pause incoming triggers and stop every controller and runner. Preserve the
   failed store for investigation. Keep the latest live `credential-epoch`
   separately before copying any old files.
2. For SQLite, restore the backup home to a **new** absolute path. Keep its
   database and WAL sidecars together. Restore its encryption keys and mounted
   credentials, then supply the latest epoch in a separate persistent directory.
   Point `SPARKWING_HOME` at this restored home:

   ```bash
   set -eu
   test ! -e /srv/sparkwing-recovered
   cp -a /srv/backups/sparkwing/pre-upgrade/controller /srv/sparkwing-recovered
   sqlite3 /srv/sparkwing-recovered/state.db 'PRAGMA integrity_check;'
   ```

   For PostgreSQL, create a new empty database and restore it, or use the PITR server:

   ```bash
   pg_restore --no-owner --no-privileges --exit-on-error \
     --dbname='service=sparkwing-recovery' /srv/backups/sparkwing/pre-upgrade/controller.dump
   ```

   Set the recovery credential directory's `pg-url` to that database. Verify the
   schema with SQL and choose its matching controller before opening it.
3. With all controllers stopped, prepare the restored store. These flags must
   agree with startup. Use `--store=postgres` for PostgreSQL:

   ```bash
   export SPARKWING_HOME=/srv/sparkwing-recovered
   sparkwing-controller restore prepare --store=sqlite \
     --credentials-dir=/srv/sparkwing-recovery-credentials \
     --credential-epoch-dir=/srv/sparkwing-live-epoch
   ```

4. Review changes since the restore point using the external audit stream and
   reapply lost revocations, removals and demotions. Reconcile external billing
   and object storage. Complete the acceptance checks in isolation. Stop the
   validation controller again before acknowledgement.
5. Acknowledge only when ready to resume scheduling:

   ```bash
   sparkwing-controller restore acknowledge --store=sqlite \
     --credentials-dir=/srv/sparkwing-recovery-credentials \
     --credential-epoch-dir=/srv/sparkwing-live-epoch
   ```

6. Start the controller with those same paths and its normal startup flags.
   Switch traffic to it, reconnect runners and resume triggers. Verify a new
   run, a scheduled run and an external trigger. Keep the failed store and
   recovery evidence until the incident is closed.

Preparation cancels restored unfinished scheduled runs and holds new schedule
admissions. Sessions, API tokens, runner and machine credentials, and API/local
schedule consent remain valid. `restore acknowledge` discards schedule catch-up
through its timestamp and lifts the hold without requiring new consent.
Preparation and startup refuse a database recorded under a different epoch,
such as one taken before an earlier sign-out or one served with a lost epoch
file, and name the file to restore.

For a security incident, such as a stolen database or leaked keys, use
`restore prepare --sign-out-everyone`. It rotates the external epoch and
invalidates all earlier credentials and API/local schedule consent. Supply a
fresh bootstrap seed, reissue credentials, and rearm those schedules.

A backup can revive credentials revoked after it was taken. Keep the controller's
audit log stream outside the database and reapply revocations, member removals
and demotions since the backup using its token prefixes, account IDs, machine
IDs and teams. Session logout and replacement records include `session_digest`,
`revoked_account_id` and `revoked_team` for the session that ended, so another
session for the same account can stay valid. Identity unlink adds
`kept_session_digest` and records each ended session on its own audit line.
Payment recovery and targeted revocation recovery are manual. Follow the
operator runbook kept outside this repository.

After a restore, Cloud nodes may still hold orphan reservations or bindings for
awards the restored controller no longer recognizes. The launcher excludes those
nodes and quarantines them when their protected team identity matches: it cordons
the node and adds the `sparkwing.dev/quarantined` taint and annotation. Healthy
nodes can still take work. Inspect the launcher logs and the node's quarantine
annotation, then terminate the node so fresh capacity replaces it. To reuse a node,
first remove the orphan workload and its stale reservation or binding, then remove
the quarantine taint and annotation and uncordon it. Uncordoning alone does not
make it eligible again. Do not clear a live award's reservation or binding.

## Acceptance checks

Keep a before/after inventory without logging secret values or bearer tokens.
Run these checks after rehearsal, upgrade and restore:

- `GET /api/v1/health` returns HTTP 200. With an operator credential, inspect
  `GET /api/v1/operator/health` for database, encryption and integration problems.
- Sign in with an existing team account. Check its team, role and membership.
  Authenticate with an existing API token and verify its grants and repository
  scope. A normal restore retains these credentials when the epoch matches.
- List secret names and scopes, then execute an isolated smoke pipeline that
  consumes a canary secret and compares its value without printing it. Listing
  encrypted rows alone does not prove that the key decrypts them.
- Reconnect an enrolled runner using its existing credential. Confirm its name,
  scope and labels, then execute a job on it. An offline inventory row alone
  does not prove that a runner can authenticate.
- Verify GitHub App installation and repository bindings, schedule definitions
  and subscriptions. Check the restore hold before acknowledgement. After
  acknowledgement, confirm only new schedule occurrences run, without catch-up
  from the lost interval. Test integrations against a disposable repository.
- Read a retained log or artifact from its restored object store, and submit a
  new run through the CLI. Confirm the controller reports the intended image
  and schema. Record any missing records relative to the chosen restore point.

## Sparkwing Cloud operator recovery

### Failed deployment after a schema upgrade

Treat a database-newer-than-binary startup error as a roll-forward incident.
A deployment that automatically restored an old image after a migration caused
a production crash loop. Deployment automation now retains candidate images
when the schema changed or its pre-deploy version cannot be established, and
reports that roll-forward is required. Check the actual database schema even
when an earlier deployment reported failure.

1. Serialize deployment work. Record the failing run, actual controller images,
   GitOps revision, database schema and last healthy revision. Preserve logs.
2. Select a repaired image that supports the database already on disk. Verify
   the image set and migration on staging. Keep the deployment's credentials,
   epoch PVC and database endpoint.
3. Commit the repaired image set to the Cloud GitOps repository, then let ArgoCD
   sync that revision. Verify ArgoCD's applied revision, pod image digests,
   rollout readiness and controller health. A direct image patch alone will be
   replaced by reconciliation.
4. Run the acceptance checks and the Cloud deploy smoke. Record the GitOps
   recovery commit and the database schema. Resume other promotions only when
   that revision is healthy. Use database restore only when roll-forward cannot
   repair the incident or data itself needs recovery.

### RDS point-in-time restore

Use the deployed RDS PostgreSQL instance's automated backups. Check retention
and the automated backup's `RestoreWindow` before choosing a UTC target.
AWS restores to a new instance and endpoint. The source stays intact. Follow
[the RDS PITR procedure](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_PIT.html).

1. Pause promotions and external admissions. Drain or stop runners and stop
   every controller that can write the source. Save the latest epoch and
   deployment credential references outside the database.
2. Record the source instance settings and select a target before the damaging
   transaction. Restore into the same private network with the required subnet
   group, security groups, encryption access and database parameter group.
   Use deployment-specific names in this example:

   ```bash
   aws rds describe-db-instance-automated-backups --db-instance-identifier "$source_db" \
     --query 'DBInstanceAutomatedBackups[].{Window:RestoreWindow,Days:BackupRetentionPeriod}'
   aws rds restore-db-instance-to-point-in-time \
     --source-db-instance-identifier "$source_db" \
     --target-db-instance-identifier "$recovery_db" \
     --restore-time "$restore_time_utc" \
     --db-subnet-group-name "$subnet_group" \
     --vpc-security-group-ids "$security_group" \
     --db-parameter-group-name "$parameter_group" \
     --no-publicly-accessible
   aws rds wait db-instance-available --db-instance-identifier "$recovery_db"
   aws rds describe-db-instances --db-instance-identifier "$recovery_db" \
     --query 'DBInstances[0].Endpoint'
   ```

3. Check the recovered schema using SQL before starting Sparkwing. Select a
   controller that opens that schema or upgrades it through a supported path.
   Restore database access and the original encryption keys. Retain the live
   epoch rather than rewinding it with the database.
4. Run `restore prepare --store=postgres` from the matching controller image
   with a private credential directory pointing at the recovery endpoint and
   the live epoch directory. Keep normal controllers stopped. Reconcile lost
   access revocations, payments and orphan Cloud nodes as described above.
5. Validate the recovered database on an isolated controller. Stop it, run
   `restore acknowledge --store=postgres`, then update the deployment's database
   secret through its managed secret source. Commit any endpoint reference or
   image changes to GitOps, sync, and start the controllers on the new endpoint.
   Check that no pod still connects to the source database before resuming
   runners, webhooks and schedules.
6. Run acceptance and Cloud smoke checks, verify backups are enabled on the
   recovered instance, and record the achieved restore time and lost interval.
   Keep the source until incident review authorizes retirement. Reconcile the
   database resource identity with infrastructure management before its next
   apply, so it cannot undo the cutover.

The repository backup drill proves SQLite snapshot recovery and PostgreSQL logical
dump recovery. RDS PITR, Cloud GitOps reconciliation, external GitHub delivery
and production payment reconciliation require separate operator exercises.

## Local restart secrets and backups

Requester secrets for a local run are encrypted in `state.db` with the existing
local secrets key before intake returns. Only that run's live execution award
can read them. Controller restarts retain the values; reaching a terminal run
state deletes them in the same transaction. A missing or unreadable value
refuses the read and asks the requester to submit a new run.

The filesystem object URL signing key is also encrypted in store settings.
Outstanding upload and download URLs keep working after a controller restart,
subject to their original expiry. Preserve the local secrets key with the
store; losing it prevents the controller from opening its signing key. If
`SPARKWING_HOME` selects an isolated home and no key exists, set
`SPARKWING_SECRETS_KEY_FILE` to a private file inside that home before starting
the controller. This keeps key creation inside the selected home.

SQLite backups contain encrypted requester secrets and signing material.
Deleting a run's live secrets does not erase earlier backups or SQLite's freed
pages. Retain and protect backups under the same policy as other encrypted
secrets. Restoring one requires the key that encrypted it. After key rotation,
older backups still require the retired key; live rows use the current key.

## Schema compatibility

Keep the binary matching a database backup. Before 1.0, schema changes may
require a reset after preserving configured data as described below. The binary
refuses a mismatched store and leaves it unchanged. For an older local store it
names the move-aside remedy. A newer database needs its matching binary, and
rollback needs a matching backup in a separate database: an older binary refuses
an upgraded store.

Before a binary upgrades a local SQLite store, it copies the store beside it as
`<store>.schema-<N>-<YYYYMMDDTHHMMSSZ>.bak`, where N is the schema version
before the upgrade. The copy is taken under the upgrade's write lock, so it
matches the state the upgrade started from. It has mode 0600 on Linux and macOS
and the store directory's ACL on Windows. It holds the same encrypted secrets
as the store. After a successful upgrade, only the two newest schema backups are
kept, ordered by modification time. A failed upgrade preserves all backups.
A pruning failure is logged and does not prevent the upgraded store from opening.
When the copy fails,
the binary refuses the upgrade and leaves the store unchanged; free disk space
and start again. To return to the older binary, stop the controller, move the
store and its `-wal` and `-shm` files aside, and copy the newest backup for that
binary's schema to the store path. Runs recorded after the backup are lost.
PostgreSQL stores are not copied: take a snapshot before starting a release that
upgrades one.

The recorded baseline digest must also match the binary at the same schema
version. A differing digest refuses startup with both digests and the backup and
reset remedy below. Stores without a digest are stamped only when every table,
view and explicit index declared by the baseline exists; missing objects refuse
startup without changing the store.

Schemas 30 through 33 upgrade in place to schema 34 when their recorded baseline
digest matches. The open transaction holds PostgreSQL's migration advisory lock,
so concurrent controllers apply each step once. Schema 30 is the oldest accepted
baseline. Earlier versions are refused unchanged.

A database reset must preserve secrets and their encryption keys, teams and
memberships, tokens, runner registrations, GitHub App installations, schedules
and subscriptions. Run history, logs and caches may be discarded. Billing records
require separate review. Do not reset a configured database until that data has
a verified recovery path.

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 29 is older than this binary (expects 36); back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 29 is older than this binary (expects 36); back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 30 is older than this binary (expects 36); start the controller with this binary once to upgrade the v0.69.2 store in place, then run this again

sparkwing: store schema version 30 baseline digest "<recorded-digest>" is not the v0.69.2 baseline "d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.69.2 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 30 baseline digest "<missing>" is not the v0.69.2 baseline "d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4" this binary upgrades, so it left the store unchanged; open it once with Sparkwing v0.69.2 to record its digest, then start this binary; or back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again

sparkwing: store schema version 37 is newer than this binary (expects 36); start a binary matching the store, or restore a backup matching this binary: stop the controller, move <path> and any <path>-wal and <path>-shm files aside, and copy the newest <path>.schema-36-*.bak to <path>; runs recorded since that backup are lost

sparkwing: store schema version 37 is newer than this binary (expects 36); start a binary matching the store, or restore a backup matching this binary into a separate database

sparkwing: store schema version 36 baseline digest "<recorded-digest>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, move <path> and any <path>-wal and <path>-shm files aside and start again

sparkwing: store schema version 36 baseline digest "<missing>" differs from this binary's "<binary-digest>"; back up the store and use a binary matching it; this build cannot upgrade it; preserve secrets, configured identities and automation before any reset, and review billing records separately; to start fresh, point the server at an empty database and start again
```
<!-- END GENERATED SCHEMA ERRORS -->
