### Fixed

- **land:** Keep reviewer prompts valid UTF-8 without hiding text changes
  Text-like patches escape invalid bytes and literal backslashes with a decoding
  rule. Binary summaries identify unreviewed content, exact blobs, modes, sizes
  and SHA-256 hashes, with safe inspection commands and an inspection requirement
  before approval. Binary summaries block independently of model output and
  cached verdicts. Only non-strict, non-executable files without a shebang and
  with a known asset extension remain summary-only: png, jpg, jpeg, gif, webp,
  ico, svgz, woff, woff2, ttf, otf, pdf, zip, gz and tgz. There is no override for
  that block; `--skip` refuses it. Raw Git filenames, including colons, match
  strict routes literally; only model locations parse numeric line/column suffixes.
  Strict review includes
  both endpoints of renames and copies from a NUL-delimited name-status inventory,
  preserving raw filenames and literal path selection.
  Repository diff attributes cannot suppress readable text changes. Ordinary UTF-8
  text hunks keep their bytes, and truncation respects character boundaries.
