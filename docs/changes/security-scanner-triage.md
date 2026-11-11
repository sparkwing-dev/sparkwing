### Security

- **cache + runner:** Bound gzip trailer verification after tar EOF
  Dependency-cache and prepared-source restores reject more than 1 MiB of trailing
  data while still verifying gzip checksums before publishing restored files.

- **deployment:** Keep temporary-directory cleanup inside its opened root
  Symlink swaps during stale-directory cleanup cannot change permissions outside
  the directory being removed.
