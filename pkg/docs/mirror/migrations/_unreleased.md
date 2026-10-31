# Migrating to the next release

## Recreate the store for schema baseline 28

This release changes the SQLite and PostgreSQL baseline directly. There is no
forward migration. Stores at another baseline refuse startup and remain
unchanged. Staging and production require a schema wipe.

Before upgrading, stop controllers and runners and back up the database. For a
disposable SQLite store, move the database and any WAL/SHM files aside. For
PostgreSQL, point the controller at a new empty database. Recreate teams,
secrets, credentials and schedules, then enroll runners again. Keep the backup
with its matching binary if its history is needed. See
[backup and restore](../backup-restore.md#schema-compatibility).

The baseline removes unused session, capability, node, trigger and storage
columns and the insert-only team counter table. It requires complete financial
decisions, nonempty credit grant references, positive award generations and
one open billing interval per award.

## Capacity profiles report measured data

Sizing and capacity responses drop stored resource pins, demand floors,
queue-wait percentiles and contention counters. Update clients that decode
those fields or the pin/floor estimate sources. The console and CLI continue
to show measured resource distributions and predecessor estimates. Local daemon
admission resource pins retain their existing behavior.
