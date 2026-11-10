### Fixed

- **Local controller:** Keep the configured secrets key and configuration paths when installing a login service, so forced restart can reopen its signing key
  Login services require a key file instead of keys supplied by environment value.
