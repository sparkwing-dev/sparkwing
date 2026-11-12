### Fixed

- **land:** Keep reviewer prompts valid UTF-8 without hiding text changes
  Text-like patches escape invalid bytes and literal backslashes with a decoding
  rule. Binary summaries identify unreviewed content, exact blobs, modes, sizes
  and SHA-256 hashes, with safe inspection commands and an inspection requirement
  before approval. Strict review includes both sides of renames as deletion and
  addition, preserving raw filenames through NUL delimiters and literal paths.
  Repository diff attributes cannot suppress readable text changes. Ordinary UTF-8
  text hunks keep their bytes, and truncation respects character boundaries.
