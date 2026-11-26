## Upgrade the store in place to schema baseline 44

Store schema 44 adds the `limit_events` table, which records one row for each
event a daily or hourly limit counts. A row keeps the limit's name, the
SHA-256 digest of the key it counts, such as a team name, an account ID or an
email address, and the event time, so it holds no raw identifier. The
retention pass deletes a row once it is older than the longest limit window.

The upgrade copies every `invitation_email_log` row into `limit_events`, once
for the recipient address and once for the inviting account when the row
names one, then drops `invitation_email_log`. The per-address and per-account
invitation email caps therefore keep counting the sends of the last 24 hours.
The daily invitation count per team starts empty, so each team's 24-hour
invitation window starts afresh at the upgrade.

The controller upgrades a schema 43 store automatically; SQLite creates its
usual backup first. Binaries without schema 44 support refuse the upgraded
store; use a matching binary or restore its pre-upgrade backup into a separate
store.
