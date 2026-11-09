### Removed

- **controller (Breaking):** Remove store upgrades from schemas 30 through 33
  Schema 34 and later retain their upgrade paths and automatic SQLite backups.
  Older stores are refused unchanged. Preserve configured data with a binary
  that supports the store before moving to this build.
