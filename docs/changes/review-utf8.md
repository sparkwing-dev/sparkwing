### Fixed

- **land:** Keep reviewer prompts valid UTF-8 without hiding text changes
  Text-like patches escape invalid bytes and carry an encoding marker. Binary
  assets show their paths, sizes and SHA-256 hashes. Strict review retains
  filenames with spaces or invalid bytes through NUL delimiters and literal
  paths. Valid text patches stay unchanged and truncate at a character boundary.
