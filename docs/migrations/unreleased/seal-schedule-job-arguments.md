## Schedule and job argument encryption

Keyed stores seal schedule arguments and job-level `WithArgs` defaults using the
run-argument keyring.

## Upgrade the store in place to schema baseline 41

The store upgrades schema 40 to 41 in the open transaction
on SQLite and PostgreSQL. This version fence protects the changed JSON format even though the
SQL table layout stays the same. Older binaries refuse the newer store rather
than misreading encrypted arguments. SQLite takes the normal pre-upgrade backup.
A failed deployment after this schema increase requires database recovery rather
than automatic rollback to the older binary. Runner transport and plaintext hash
binding stay unchanged.

Before upgrading, back up the database and its secrets keys. Stop writes and
schedule ticks while replacing every controller reader and writer with the new
version. Resume them together. Keep the current and previous keys mounted while
rotation runs.

The schema upgrade leaves existing plaintext argument rows readable until
resealed. Node specs encrypt only argument default values so SQL predicates and
metadata readers continue to work for live runs. Schedule envelope versions are
numeric, so string argument names such as `sealed_arguments` remain valid. Run
`sparkwing operator secrets rotate` after upgrading all replicas. It seals the
existing schedule declarations, overrides, override baselines, consent requests,
fire history, plans, node specs and expansion bodies in transactions. An
interrupted rotation can be repeated. The existing `plaintext_run_arguments`
count includes these documents. Run `sparkwing operator secrets rotate --verify`
until it reports `plaintext_run_arguments: 0`, `previous_key_only: 0` and no
unreadable rows, then retire the previous key. Verification writes nothing.

A keyless controller still stores argument values unencrypted. Configure its
secrets key before rotation. Retained backups and database free pages, WAL and
dead tuples can retain old plaintext after rows are sealed. Protect those copies
through their retention period. Downgrade by restoring a matching pre-upgrade
backup and its keys with writes stopped, rather than opening encrypted rows with
an older controller.
