### Fixed

- **land:** Keep reviewer prompts valid UTF-8 without hiding text changes
  Text-like patches escape invalid bytes and literal backslashes with a decoding
  rule. Binary summaries identify unreviewed content, exact blobs, modes, sizes
  and SHA-256 hashes, with safe inspection commands and an inspection requirement
  before approval. A binary summary on a strict route, an executable mode or a
  script/source path adds a blocking finding independently of model output,
  including cached verdicts. There is no override for that block; `--skip` refuses
  it. Ordinary binary assets elsewhere remain summary-only. Strict review includes
  both endpoints of renames and copies from a NUL-delimited name-status inventory,
  preserving raw filenames and literal path selection.
  Repository diff attributes cannot suppress readable text changes. Ordinary UTF-8
  text hunks keep their bytes, and truncation respects character boundaries.
