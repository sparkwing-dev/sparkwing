# Backup, restore, and upgrade

How to take a backup of a self-hosted controller, prove the backup is
good, and upgrade the controller without betting the install on the
migration working. Every command here is meant to be typed as written.

## What controller state is

A restore needs all of the following. A backup that skips any of it
restores a controller that starts and still cannot do its job.

- **The state database.** SQLite at `$SPARKWING_HOME/state.db`, or the
  PostgreSQL database named by `SPARKWING_PG_URL`. It holds runs,
  nodes, events, secrets, credits, API tokens, dashboard users, cron
  schedules, and the schema version the binary matches itself against.
- **The secrets key**, `SPARKWING_SECRETS_KEY` or the file behind
  `--secrets-key-file`. Secret values are sealed in the database under
  it. Restoring the database without the key restores rows nothing can
  open. Started with some other key, the controller refuses to start
  once that key opens none of the values it samples; started with no key,
  it starts and answers each read of a sealed value with an error.
  During a key rotation the same is true of
  `SPARKWING_SECRETS_PREVIOUS_KEY`.
- **The controller's own start-up configuration**: its flags and the
  rest of its environment, including `GITHUB_WEBHOOK_SECRET`,
  `GITHUB_WEBHOOK_BINDINGS`, `GITHUB_TOKEN`, and the announced service
  URLs. On Kubernetes that is the Helm values file and the Secrets it
  names; on a single machine it is the unit file and its environment.

What the state database does not hold, and what a restore therefore
does not bring back:

- Run log bodies, which the logs service owns.
- Node artifacts and caches, which the object store owns.
- Mirrored git repositories, which the gitcache owns. These rebuild
  themselves from their upstreams.

Back those up on their own schedule, or accept losing them; the
controller serves run and node records without them.

## Back up

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
printf %s "$SPARKWING_SECRETS_KEY" > "$DEST/secrets.key"
chmod 600 "$DEST"/*
( cd "$DEST" && sha256sum ./* > SHA256SUMS )
```

A controller that stopped cleanly checkpoints its write-ahead log and
removes `state.db-wal` and `state.db-shm`, so those copies usually find
nothing. Copy them when they are there, because a controller that was
killed leaves committed transactions in the log and a restore without
it loses them.

### PostgreSQL

`pg_dump` reads a consistent snapshot of a live database, so the
controller can keep serving while it runs.

```bash
STAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
DEST=/var/backups/sparkwing/$STAMP
install -d -m 700 "$DEST"
pg_dump --format=custom --no-owner --no-privileges \
    --file="$DEST/controller.dump" "$SPARKWING_PG_URL"
printf %s "$SPARKWING_SECRETS_KEY" > "$DEST/secrets.key"
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

SPARKWING_HOME="$SCRATCH/controller-home" \
SPARKWING_SECRETS_KEY="$(cat "$DEST/secrets.key")" \
    sparkwing-controller --addr 127.0.0.1:4456
```

For a PostgreSQL backup, restore the archive into a database of its own
and point the scratch controller at that instead:

```bash
createdb sparkwing_verify
pg_restore --no-owner --no-privileges --exit-on-error \
    --dbname="postgres://.../sparkwing_verify" "$DEST/controller.dump"

SPARKWING_PG_URL="postgres://.../sparkwing_verify" \
SPARKWING_SECRETS_KEY="$(cat "$DEST/secrets.key")" \
    sparkwing-controller --addr 127.0.0.1:4456
```

The controller prints the schema it opened as its first line:

```text
sparkwing-controller: version vX.Y.Z, runs-store schema NN, commit ...
```

Then run the checks in [Verify the install](#verify-the-install)
against the scratch controller. When they pass, stop it and delete the
scratch directory and the verification database. Starting a controller
against a copy migrates that copy to the binary's schema, which is why
this is done on a copy and never on the archive itself.

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

Start the controller with `SPARKWING_SECRETS_KEY` set to the key from
the same backup directory, and with `SPARKWING_PG_URL` pointing at the
restored database. Then run [Verify the install](#verify-the-install).

Keep `state.db.displaced` and the old database until the verification
passes. They are the only copy of whatever the install did between the
backup and the restore.

## Upgrade

Work through the stages in order. Each one names what rollback means
while you are standing in it.

### Read what the release changes

```bash
sparkwing docs migrations list
sparkwing docs migrations read --version <vX.Y.Z>
```

A guide exists only for a release carrying a breaking change. It names
whether that release's migration adds to the schema or rebuilds a
table, which is the difference between the two rollback stories below.

*Rollback:* nothing has happened yet.

### Stop the controller and back up

Stop the controller, take the backup for your database from
[Back up](#back-up), and run [Verify the backup](#verify-the-backup) on
it. Verify before you migrate, not after, because after the migration
the backup is the only way back and a bad one is discovered too late.

*Rollback:* start the controller you already have.

### Rehearse the migration on a copy

Do this before the live database is touched, because it is the only way
to learn what rollback will mean while rollback still means nothing.

Restore the backup into a scratch directory the way
[Verify the backup](#verify-the-backup) does, then run the **new**
controller against that copy. It migrates the copy and prints the schema
it reached:

```bash
SPARKWING_HOME="$SCRATCH/controller-home" \
SPARKWING_SECRETS_KEY="$(cat "$DEST/secrets.key")" \
    <new sparkwing-controller> --addr 127.0.0.1:4456
```

Stop it, and start the **previous** controller against the same migrated
copy. A binary opens a database whose schema version is higher than its
own as long as it understands every requirement the database lists. A
migration that only adds columns, tables or indexes lists no new
requirement, so the previous binary starts. A migration that the
previous binary cannot be trusted with stamps a requirement, and that
binary refuses:

```text
sparkwing: this state database uses <requirement>, which needs sparkwing >= vX.Y.Z;
you have vA.B.C. Run `sparkwing update` to upgrade.
```

Starting is not the whole test. A migration that rebuilds a table's
primary key leaves the previous binary able to start and unable to
write, because its upserts name a unique index that no longer exists.
Register a profile against the scratch controller and write through it:

```bash
sparkwing configure profiles add --name rehearsal --controller http://127.0.0.1:4456 --token-stdin
sparkwing secrets set --profile rehearsal --name ROLLBACK_PROBE --value probe
sparkwing secrets delete --profile rehearsal --name ROLLBACK_PROBE
```

A probe that is stored and deleted means binary rollback is real. A
probe that fails means it is not, whatever the start-up line said. Stop
the scratch controller and delete its directory either way.

*Rollback:* nothing has happened yet.

### The point of no return

**The migration that rebuilds a primary key is the point of no return.**
Before it, rollback means reinstalling the previous binary and starting
it against the same database. After it, rollback means restoring the
backup, and every run, credit charge and secret change recorded since
the backup is gone with it.

The release's migration guide names which migrations rebuild a table.
When you cannot tell, treat the upgrade as past the point of no return:
the cost of assuming so is a restore drill, and the cost of assuming
otherwise is the install.

Binary rollback before that point is not free either. The previous
binary writes through the columns it knows, so rows it creates against
an already-migrated database take the new columns' defaults. Those rows
stay that way when you upgrade again. Roll back to serve reads and to
buy time, and keep the window short.

### Install and start the new controller

Install the new binary or image, start it, and read its first line. It
migrates the database on open and prints the schema it reached.

*Rollback:* whichever of the two stories the stage above established.

### Verify

Run [Verify the install](#verify-the-install). Until it passes, treat
the upgrade as unfinished and keep the backup where you can reach it.

## Verify the install

Run these after a restore and after an upgrade, against the controller
you are judging. They are ordered so the first failure is the most
informative. `--profile` names the profile pointing at that controller;
register one with `sparkwing configure profiles add` when verifying a
scratch controller on a spare port.

```bash
# The controller answers, and the token still authenticates.
sparkwing configure profiles test --profile prod

# Run history is present, and reaches the most recent run you remember.
sparkwing runs list --profile prod

# One run still carries its nodes.
sparkwing runs get --run <id> --profile prod

# Secret rows are present.
sparkwing secrets list --profile prod

# The secrets key matches the database. This is the check that catches a
# database restored without its key, and nothing else catches it.
sparkwing secrets get --profile prod --name <name>

# The ledger balance and its history survived.
sparkwing cluster credits show --profile prod
sparkwing cluster credits history --profile prod

# Runner and user credentials survived, so runners reconnect without
# being re-enrolled.
sparkwing cluster tokens list --profile prod

# Runners have reconnected.
sparkwing cluster agents list --profile prod
```

Finish by running one real pipeline end to end and reading its logs,
because that exercises the claim, secret-read, credit-charge and
log-write paths together in a way no read verb does.

## Related pages

- [Self-hosting](self-hosting.md) for how the controller and its
  database are deployed.
- [Auth](auth.md) for tokens, users and scopes.
- [Security](security.md) for how secret values are sealed.
