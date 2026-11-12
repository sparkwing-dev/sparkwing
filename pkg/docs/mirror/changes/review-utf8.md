### Fixed

- **land:** Keep reviewer prompts valid UTF-8 for unmarked binary files
  Review shows their paths, sizes and SHA-256 hashes instead of raw bytes,
  preserves text patches and truncates at a character boundary.
