### Fixed

- **Logs:** Filesystem controllers use the log-slot clock when signing uploads, preventing upload failures after a backward system clock step while retaining the fifteen-minute lifetime limit.
