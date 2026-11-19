### Fixed

- **Logs:** Filesystem controllers use the log-slot clock when signing uploads, preventing upload failures after a backward system clock step while retaining the fifteen-minute lifetime limit.

- **Logs:** S3 upload signing caps URLs at fifteen minutes when the store clock runs ahead of the controller host, preventing clock-skew failures
