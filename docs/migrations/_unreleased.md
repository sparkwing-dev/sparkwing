# Migrating to the next release

No breaking changes so far.

## The pipeline linter reaches further

`sparkwing pipeline lint` follows a `Plan` body one level into a package-level
helper it calls, so I/O a `Plan` delegates is reported rather than missed. It
also treats a `sparkwing/services` call inside `Plan` as plan-time I/O, as it
already did for `sparkwing/docker` and `sparkwing/git`.

Neither rule changes what a pipeline does. Both can report a pipeline that
this release does not otherwise change, which is the point: the work belongs
in a Job or Step body, where it runs at dispatch.
