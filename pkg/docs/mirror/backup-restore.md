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
- **The secrets key**: the `secrets-key` file in the controller's
  `--credentials-dir`, or, on a single-team SQLite controller given none,
  the `secrets.key` it created beside `state.db`. Secret values are sealed
  in the database under it. Restoring the database without the key restores rows nothing can
  open. Started with some other key, the controller refuses to start
  once that key opens none of the values it samples, and started with no
  key it refuses to create a new one beside sealed values.
  During a key rotation the same is true of `secrets-key.previous`.
- **The controller's own start-up configuration**: its flags and the
  rest of its credentials directory, including the GitHub App settings
  and the announced service URLs. On Kubernetes that is the Helm values file and the Secrets it
  names; on a single machine it is the unit file and its environment.

What the state database does not hold, and what a restore therefore
does not bring back:

- Node log segments, which the controller keeps in its cache bucket or
  output directory (see [self-hosting](self-hosting.md#node-log-retention)).
- Node artifacts and caches, which the object store owns.
- Mirrored git repositories, which the gitcache owns. These rebuild
  themselves from their upstreams.

Back those up on their own schedule, or accept losing them; the
controller serves run and node records without them.

## Back up

The commands below write `$CREDS` for the controller's `--credentials-dir`.
A single-team SQLite controller that was never given a `secrets-key` keeps
the key it created at `$SPARKWING_HOME/secrets.key`; the copy falls back to
that file.

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
a real restore, aimed at a scratch directory and a spare port, and it
changes nothing on the live install.

```bash
DEST=/var/backups/sparkwing/<stamp>
( cd "$DEST" && sha256sum -c SHA256SUMS )

SCRATCH=$(mktemp -d)
install -d -m 700 "$SCRATCH/controller-home"
cp -p "$DEST/state.db" "$SCRATCH/controller-home/state.db"
cp -p "$DEST"/state.db-wal "$SCRATCH/controller-home/" 2>/dev/null || true
cp -p "$DEST"/state.db-shm "$SCRATCH/controller-home/" 2>/dev/null || true
install -d -m 700 "$SCRATCH/credentials"
cp -p "$DEST/secrets.key" "$SCRATCH/credentials/secrets-key"

SPARKWING_HOME="$SCRATCH/controller-home" \
    sparkwing-controller --addr 127.0.0.1:4456 --credentials-dir "$SCRATCH/credentials"
```

For a PostgreSQL backup, restore the archive into a database of its own
and point the scratch controller at that instead:

```bash
createdb sparkwing_verify
pg_restore --no-owner --no-privileges --exit-on-error \
    --dbname="postgres://.../sparkwing_verify" "$DEST/controller.dump"

(umask 077 && printf %s "postgres://.../sparkwing_verify" > "$SCRATCH/credentials/pg-url")
sparkwing-controller --addr 127.0.0.1:4456 --credentials-dir "$SCRATCH/credentials"
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

Start the controller with its `secrets-key` credential holding the key
from the same backup directory, and its `pg-url` credential naming the
restored database. Then run [Verify the install](#verify-the-install).

Keep `state.db.displaced` and the old database until the verification
passes. They are the only copy of whatever the install did between the
backup and the restore.

## Upgrade

Until paid launch the store has one baseline schema and no upgrade path.
A controller opens a database at its own schema version, creates the
baseline in an empty one, and refuses any other:

```text
sparkwing: this store predates the baseline (it records schema version NN, this build creates version 1 and upgrades none); delete <path> (or drop the database) and start again
```

An upgrade between two builds at the same schema needs nothing beyond
installing the new binary or image and starting it. Its first line names
the schema it opened:

```text
sparkwing-controller: version vX.Y.Z, runs-store schema 1, commit ...
```

An upgrade to a build at another schema, including the first build with
the baseline, discards the database: stop the controller, delete
`$SPARKWING_HOME/state.db` with its `-wal` and `-shm` files or drop and
recreate the PostgreSQL database, then start the new controller. Runs,
credit history, secrets and credentials go with the database, so add the
secrets and runner tokens again. A backup taken before the upgrade
restores only under a controller at the old schema.

*Rollback:* start the previous controller against the backup from
[Back up](#back-up).

## Verify the install

Run these after a restore and after an upgrade, against the controller
you are judging. They are ordered so the first failure is the most
informative. `--profile` names the profile pointing at that controller;
register one with `sparkwing profiles add` when verifying a
scratch controller on a spare port.

```bash
# The controller answers, and the token still authenticates.
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

# Runner and user credentials survived, so runners reconnect without
# being added again.
sparkwing tokens list --profile prod

# Runners have reconnected.
sparkwing runners list --profile prod
```

Sparkwing Cloud operators also verify the restored credit balance and ledger
history with the private `sparkwing-ops` tool.

Finish by running one real pipeline end to end and reading its logs,
because that exercises the claim, secret-read, credit-charge and
log-write paths together in a way no read verb does.

## Related pages

- [Self-hosting](self-hosting.md) for how the controller and its
  database are deployed.
- [Auth](auth.md) for tokens, users and scopes.
- [Security](security.md) for how secret values are sealed.
