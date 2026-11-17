## Upgrade the store in place to schema baseline 40

Schema 40 installs OIDC audit privacy reconciliation. Supported stores at schema
30 through 39 upgrade in place through the existing digest-checked migration
chain. Stop older controllers, back up the database and external secrets keys,
and start the new controller with its normal secrets keyring. After schema setup,
policy configuration scrubs historical OIDC audit rows before HTTP serving.
A keyless diagnostic store open can upgrade the schema but leaves reconciliation
pending. Use configured controller startup to finish it.

Before, `oidc.minted` attributes stored raw `audience` and `subject`. After, they
store `audience_digest`, `subject_digest` and `digest_key_id`. Update audit
consumers to compare both the key ID and the field's HMAC-SHA-256 digest.
Issued JWT claims, token ID, run ID, node digest and expiry keep their meanings.
See the OIDC guide's Audit investigation section for the offline recipe.

Reconciliation admits only exact precomputed changes to those historical payloads
under schema 40 while migration version 1 is pending. Every identity, timestamp,
actor and other column stays fixed. Other updates and retention-protected deletes
remain refused. The transaction restores the unconditional update guard before
writing its `audit.oidc_scrubbed` receipt and marking completion. A failure rolls
all these changes back. Retry startup with the keyring available. A malformed
historical payload requires operator investigation before startup can finish.

Rotation writes new rows with the new key ID. It never rewrites older digests.
Retain retired keys in restricted external escrow if those rows must remain
matchable after the previous key leaves the online keyring. Database-only exports
cannot test candidates, but a key holder can. Account deletion preserves these
pseudonymous digests and tracing IDs through the configured audit horizon.

SQLite's automatic pre-upgrade `.bak` file contains the old raw data. Historical
backups, WAL, PostgreSQL dead tuples, snapshots and replicas need their own
retirement. Restoring an older copy brings the raw data back until the new
controller reconciles it again. Do not downgrade an upgraded database. To run an
older binary, restore its matching backup into a separate database and treat its
raw audit contents as retained personal data. The backup and restore guide describes retention duties.
