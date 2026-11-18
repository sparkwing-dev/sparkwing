## Automatic startup sealing

Controllers and local daemons seal legacy plaintext argument rows in the
background after startup when a usable secrets key is configured. Upgrading to
this release needs no manual reseal step. Readiness remains available. Inspect
the startup sealing summary for sealed, already sealed and unreadable counts.
Operational failures retry with backoff. A missing key produces one skipped
summary and audit event. Configure or restore the key and restart to enable the
pass.

Startup leaves existing envelopes unchanged. Retiring a key still requires
`sparkwing operator secrets rotate`, followed by `--verify` until
`plaintext_run_arguments: 0`, `previous_key_only: 0` and no unreadable rows remain.
Malformed argument documents are counted as unreadable and skipped by rotation
and verification. Repair them before retiring a key.

Users staying on v0.76.0 must follow its migration guide and run operator rotation
to seal pre-existing plaintext rows. Retained backups, database free pages, WAL
and dead tuples may retain old plaintext after rows are sealed. Protect those
copies through their retention period.
