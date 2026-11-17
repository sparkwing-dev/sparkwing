## Schedule and job argument encryption

Keyed stores seal schedule arguments and job-level `WithArgs` defaults using the
run-argument keyring. This changes the stored JSON format without changing the
SQL baseline or schema version. Older controller binaries cannot read the new
argument documents. Runner transport and plaintext hash binding stay unchanged.

Before upgrading, back up the database and its secrets keys. Stop writes and
schedule ticks while replacing every controller reader and writer with the new
version. Resume them together. Keep the current and previous keys mounted while
rotation runs.

Existing plaintext rows stay readable on SQLite and PostgreSQL. Run
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
