# Migrating to the next release

No breaking changes so far. One schema migration takes longer than the
others on a large database; it needs no action.

## Schema v38 builds two indexes while the controller starts

Schema v38 adds three storage tables and builds timestamp indexes on `events`
and `node_metrics`, which are usually the two largest tables. Budget roughly a
second per million rows in each. On Postgres the build takes a lock that
blocks writes to those tables for its duration, so a controller with hundreds
of millions of event rows is briefly unavailable for writes while it starts.
Nothing else is required; the migration adds no column and declares no
requirement, so a binary predating it still opens the database.
