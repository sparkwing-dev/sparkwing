# Backup, restore, and upgrade

How to take a backup of a self-hosted controller, prove the backup is
good, and upgrade the controller. Every command here is meant to be
typed as written.

## What controller state is

A restore needs all of the following. A backup that skips any of it
restores a controller that starts and still cannot do its job.

- **The state database.** SQLite at `$SPARKWING_HOME/state.db`, or the
  PostgreSQL database named by the controller's `pg-url` credential. It holds runs,
  nodes, events, secrets, credits, API tokens, dashboard users, cron
  schedules, and the schema version the binary matches itself against.
  Team purge retains payment mode and SHA-256 provider-identity hashes, with
  no team, customer or card metadata. These closure records belong in database
  backups because reconciliation uses them to distinguish purged parents from
  correlations lost during restore.
- **The secrets key**: the `secrets-key` file in the controller's
  `--credentials-dir`, or, on a single-team SQLite controller given none,
  the `secrets.key` it created beside `state.db`. Secret values are sealed
  in the database under it. Restoring the database without the key restores rows nothing can
  open. Started with some other key, the controller refuses to start
  once that key opens none of the values it samples, and started with no
  key it refuses to create a new one beside sealed values.
  During a key rotation the same is true of `secrets-key.previous`.
- **The live credential epoch.** Keep `credential-epoch` outside the database
  backup. Locally it lives beside `secrets-key`, or beside `state.db` without
  a credentials directory. `--credential-epoch-dir` selects a separate
  persistent writable directory when credentials are read-only. The Helm
  chart keeps it on a dedicated retained PVC at
  `/var/lib/sparkwing/credential-epoch`. Preserve its latest value through a restore.
  Copying an older epoch back can revive credentials revoked after the backup.
  A declared restore rotates it and invalidates every earlier bearer token and
  dashboard session. Move the latest epoch and the controller credentials
  together when moving the install to another machine.
- **The controller's own start-up configuration**: its flags and the
  rest of its credentials directory, including the GitHub App settings
  and the announced service URLs. On Kubernetes that is the Helm values file and the Secrets it
  names; on a single machine it is the unit file and its environment.

What the state database does not hold, and what a restore therefore
does not bring back:

- Tagged job-attempt and owner-unit log segments in the selected storage backend
  (see [self-hosting](self-hosting.md#node-log-retention)).
- Artifact, output and dependency-cache bytes in the selected storage backend.
- Machine-local pipeline binaries, which rebuild from source.
- Run checkouts, which fetch their own Git objects from upstream.

Back those up on their own schedule, or accept losing them; the
controller serves run and node records without them.

## Back up

The commands below write `$CREDS` for the controller's `--credentials-dir`.
A single-team SQLite controller that was never given a `secrets-key` keeps
the key it created at `$SPARKWING_HOME/secrets.key`; the copy falls back to
that file. A restored `secrets.key` must be owner-only: sparkwing refuses one
that group or others can read and names the `chmod 600` that fixes it.

### SQLite

Stop the controller first, because a file copy taken while the
controller is writing can catch the database mid-transaction.

```bash
STAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
DEST=/var/backups/sparkwing/$STAMP
install -d -m 700 "$DEST"
cp -p "$SPARKWING_HOME/state.db" "$DEST/state.db"
cp -p "$SPARKWING_HOME/state.db-wal" "$DEST/" 2>/dev/null || true
cp -p "$SPARKWING_HOME/state.db-shm" "$DEST/" 2>/dev/null || true
cp -p "$CREDS/secrets-key" "$DEST/secrets.key" 2>/dev/null ||
    cp -p "$SPARKWING_HOME/secrets.key" "$DEST/secrets.key"
chmod 600 "$DEST"/*
( cd "$DEST" && sha256sum ./* > SHA256SUMS )
```

A controller that stopped cleanly checkpoints its write-ahead log and
removes `state.db-wal` and `state.db-shm`, so those copies usually find
nothing. Copy them when they are there, because a controller that was
killed leaves committed transactions in the log and a restore without
it loses them.

For a live database in WAL mode, prepare `$DEST` as above and replace the three
file-copy commands with a backup inside a read transaction. Keep the transaction
open until the backup finishes so concurrent writes cannot restart the copy:

```bash
sqlite3 "$SPARKWING_HOME/state.db" <<SQL
.bail on
.timeout 5000
BEGIN;
SELECT count(*) FROM sqlite_schema;
.backup '$DEST/state.db'
ROLLBACK;
SQL
```

Then save the secrets key, restrict permissions and create the checksums as above.
The backup contains the committed snapshot without separate WAL or SHM files.

### PostgreSQL

`pg_dump` reads a consistent snapshot of a live database, so the
controller can keep serving while it runs.

```bash
STAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
DEST=/var/backups/sparkwing/$STAMP
install -d -m 700 "$DEST"
pg_dump --format=custom --no-owner --no-privileges \
    --file="$DEST/controller.dump" "$(cat "$CREDS/pg-url")"
cp -p "$CREDS/secrets-key" "$DEST/secrets.key" 2>/dev/null ||
    cp -p "$SPARKWING_HOME/secrets.key" "$DEST/secrets.key"
chmod 600 "$DEST"/*
( cd "$DEST" && sha256sum ./* > SHA256SUMS )
```

Run a `pg_dump` whose major version is at least the server's, because
an older `pg_dump` refuses a newer server outright rather than writing
a partial archive.

## Verify the backup

An unread backup is a guess. Restore it somewhere harmless and read the
install back out of it before you rely on it. This is the same drill as
a real restore with separate state and credentials, aimed at a scratch
directory and a spare port. Keep live runners and webhook delivery away
from the scratch controller.

```bash
DEST=/var/backups/sparkwing/<stamp>
( cd "$DEST" && sha256sum -c SHA256SUMS )

SCRATCH=$(mktemp -d)
install -d -m 700 "$SCRATCH/controller-home" "$SCRATCH/credentials"
cp -p "$DEST/secrets.key" "$SCRATCH/credentials/secrets-key"
```

For SQLite, copy the saved database:

```bash
cp -p "$DEST/state.db" "$SCRATCH/controller-home/state.db"
cp -p "$DEST"/state.db-wal "$SCRATCH/controller-home/" 2>/dev/null || true
cp -p "$DEST"/state.db-shm "$SCRATCH/controller-home/" 2>/dev/null || true
```

For a PostgreSQL backup, restore the archive into a database of its own
and point the scratch controller at that instead:

```bash
createdb sparkwing_verify
pg_restore --no-owner --no-privileges --exit-on-error \
    --dbname="postgres://.../sparkwing_verify" "$DEST/controller.dump"

(umask 077 && printf %s "postgres://.../sparkwing_verify" > "$SCRATCH/credentials/pg-url")
```

For a billing-enabled backup, put the matching `stripe-api-key` and
`stripe-webhook-secret` in the scratch credentials directory before the next
step. Complete the declared restore against the scratch database before
starting its controller. Set `STORE=postgres` for the PostgreSQL copy:

```bash
STORE=sqlite
export SPARKWING_HOME="$SCRATCH/controller-home"
sparkwing-controller restore prepare --store "$STORE" --credentials-dir "$SCRATCH/credentials" &&
    sparkwing-controller restore reconcile --store "$STORE" --credentials-dir "$SCRATCH/credentials" &&
    sparkwing-controller restore acknowledge --store "$STORE" --credentials-dir "$SCRATCH/credentials"
```

Stop if any step fails. The pull reads provider facts and grants
credits only in the scratch database; it does not create or confirm charges.
Do not copy `billing-token` or connect runners to the scratch controller.
Keep the scratch listener on loopback, avoid triggering jobs, and use the
read checks below without the final real-pipeline check.

Create a fresh `bootstrap-admin-token` seed in the scratch credentials
directory before starting, because restored credentials cannot authenticate:

```bash
(umask 077 && openssl rand -hex 32 > "$SCRATCH/credentials/bootstrap-admin-token")
sparkwing-controller --addr 127.0.0.1:4456 --store "$STORE" --credentials-dir "$SCRATCH/credentials"
```

The controller prints the schema it opened as its first line:

```text
sparkwing-controller: version vX.Y.Z, runs-store schema NN, commit ...
```

Then run the checks in [Verify the install](#verify-the-install)
against the scratch controller. When they pass, stop it and delete the
scratch directory and the verification database. A controller at
another schema refuses the copy, so verify with the build that took the
backup.

## Restore

Stop the controller before restoring, because both paths replace the
database underneath it.

SQLite:

```bash
mv "$SPARKWING_HOME/state.db" "$SPARKWING_HOME/state.db.displaced"
rm -f "$SPARKWING_HOME/state.db-wal" "$SPARKWING_HOME/state.db-shm"
cp -p "$DEST/state.db" "$SPARKWING_HOME/state.db"
cp -p "$DEST"/state.db-wal "$SPARKWING_HOME/" 2>/dev/null || true
cp -p "$DEST"/state.db-shm "$SPARKWING_HOME/" 2>/dev/null || true
chmod 600 "$SPARKWING_HOME/state.db"*
```

PostgreSQL, restoring into an empty database rather than over the live
one, because `pg_restore` into a populated database leaves a mixture of
both:

```bash
createdb sparkwing_restored
pg_restore --no-owner --no-privileges --exit-on-error \
    --dbname="postgres://.../sparkwing_restored" "$DEST/controller.dump"
```

Keep the controller stopped through preparation, reconciliation and
acknowledgment. Point `secrets-key` at the key from the backup and `pg-url`
at the restored database when using PostgreSQL. Preserve the latest live
`credential-epoch` instead of copying one from the backup. Use the serving
controller's `--credential-epoch-dir` for every restore command when it has one.
With Helm this is `/var/lib/sparkwing/credential-epoch`, on its dedicated PVC;
attach that PVC to the offline restore pod, with the controller stopped. Keep
it separate from any volume or snapshot being rolled back with the database.
The process user must own the directory and be able to write it. The controller
restricts its mode to `0700`; epoch files must pass the credential-file owner,
mode and symlink checks. Kubernetes Secret projections remain read-only.

Set `$EPOCH_DIR` to the directory selected above, or `$CREDS` for the local
default (`$SPARKWING_HOME` when no credentials directory is configured).
Declare the restore before restarting. Use `--store postgres` for PostgreSQL:

```bash
sparkwing-controller restore prepare --credentials-dir "$CREDS" --credential-epoch-dir "$EPOCH_DIR" --store sqlite
sparkwing-controller restore reconcile --credentials-dir "$CREDS" --credential-epoch-dir "$EPOCH_DIR" --store sqlite
```

Preparation rotates the external epoch, so every pre-restore bearer token
and dashboard session is refused, including credentials revoked after the
backup. It also holds schedules. Every preparation, including a retry,
rotates the epoch again. Issue replacement credentials only after the last
preparation. Reconciliation pulls provider payments through the same idempotent
settlement path as webhooks. Cloud reconciliation needs the payment mode's
provider and webhook credentials. Recovery uses webhook alerting and owner mail;
pass the serving controller's `--email-sender`, `--email-configuration-set` and
`--dashboard-url` to `restore reconcile` to send email with the same links.
Without an email sender, notification bodies go to stderr. Every sweep clears
the previous success before contacting the provider. A failed or interrupted resweep keeps
acknowledgment blocked until a new sweep succeeds. A Checkout reservation
created after the backup is absent from the restored database. Its provider metadata carries
routing identity only, so reconciliation fails rather than inventing the
missing reservation. Stop and repair the reservation with an operator before
retrying. There is no supported reservation-repair API.

Card recovery selects the latest completion using the provider's
`setup_intent.succeeded` event time, then the committed `payment_setups`
completion time when the event is unavailable. [Stripe lists events for
30 days](https://docs.stripe.com/api/events/list). If both completion records
are missing, acknowledgment stays blocked while monetary recovery continues.
A [detached payment method](https://docs.stripe.com/api/payment_methods/detach)
is retained as unusable historical evidence and is never selected as the
current card. Its detached state does not stop refunds, disputes or other
monetary recovery.

Inspect the restored state and quarantined jobs before acknowledging:

```bash
sparkwing-controller restore acknowledge --credentials-dir "$CREDS" --credential-epoch-dir "$EPOCH_DIR" --store sqlite
```

Acknowledgment advances every schedule cursor to the database's current
time and discards due instants through that time, so catch-up cannot replay
work that ran after the backup. The restore commands acquire the controller
lock and refuse to run alongside a live controller.

Before starting, replace `bootstrap-admin-token` with a fresh seed in the
credentials directory. Preparation resets the consumed bootstrap gate, so
startup provisions that new operator token. Start the controller and reissue
runner and user tokens. Users must sign in again. Run
[Verify the install](#verify-the-install) with the newly issued credentials.

Keep `state.db.displaced` and the old database until the verification
passes. They are the only copy of whatever the install did between the
backup and the restore.

## Upgrade

Until paid launch the store has one baseline schema and no upgrade path.
A controller opens a database at its own schema version, creates the
baseline in an empty one, and refuses any other:

<!-- BEGIN GENERATED SCHEMA ERRORS -->
```text
sparkwing: store schema version 19 is older than this binary (expects 20); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, delete <path> (or drop the database) and start again

sparkwing: store schema version 21 is newer than this binary (expects 20); start a binary matching the store, or restore a backup matching this binary into a separate database
```
<!-- END GENERATED SCHEMA ERRORS -->

An upgrade between two builds at the same schema needs nothing beyond
installing the new binary or image and starting it. Its first line names
the schema it opened:

```text
sparkwing-controller: version vX.Y.Z, runs-store schema <version>, commit ...
```

A build at a newer schema cannot upgrade the database. Keep the matching
binary and backup to retain its data. To replace a disposable install, stop
the controller, delete
`$SPARKWING_HOME/state.db` with its `-wal` and `-shm` files or drop and
recreate the PostgreSQL database, then start the new controller. Runs,
credit history, secrets and credentials go with the database, so add the
secrets and runner tokens again. A backup taken before the upgrade
restores only under a controller at the old schema.

*Rollback:* keep the newer database intact. Start a matching newer binary,
or restore the backup taken by the previous binary into a separate database
and follow [Restore](#restore). An older binary cannot read a newer schema.

## Verify the install

Run these after a restore and after an upgrade, against the controller
you are judging. They are ordered so the first failure is the most
informative. `--profile` names the profile pointing at that controller;
register one with `sparkwing profiles add` when verifying a
scratch controller on a spare port.

```bash
# The controller answers with a newly issued operator token.
sparkwing runs list --profile prod --limit 1

# Run history is present, and reaches the most recent run you remember.
sparkwing runs list --profile prod

# One run still carries its nodes.
sparkwing runs status --run RUN -o json --exit-zero --profile prod

# Secret rows are present.
sparkwing secrets list --profile prod

# The secrets key matches the database. This is the check that catches a
# database restored without its key, and nothing else catches it.
sparkwing secrets get --profile prod --secret NAME

# Only newly issued runner and user credentials authenticate.
# Verify a pre-restore token is refused before reconnecting runners.
sparkwing tokens list --profile prod

# Runners have reconnected.
sparkwing runners list --profile prod
```

Sparkwing Cloud operators must pull payments from the provider before
acknowledging schedule catch-up. The reconcile command scans provider payment
history, then operators verify the restored balance and ledger. Repeating reconciliation must
leave the balance unchanged. Payment recovery does not recover usage spent
after the backup; compare that interval with retained usage records before
resuming Cloud work.

For a live restore, finish by running one real pipeline end to end and reading its logs,
because that exercises the claim, secret-read, credit-charge and
log-write paths together in a way no read verb does.

## Related pages

- [Self-hosting](self-hosting.md) for how the controller and its
  database are deployed.
- [Auth](auth.md) for tokens, users and scopes.
- [Security](security.md) for how secret values are sealed.
