### Fixed

- **Landing:** affected Go tests ignore submitting shell limits
  The job reserves the shared host's command allowance and overrides process and saved Go build flags while preserving other saved Go configuration.
