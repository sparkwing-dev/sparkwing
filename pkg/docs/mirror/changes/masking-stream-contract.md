### Docs

- **secrets:** Clarify the v0.68.0 stream-masking description
  The v0.68.0 statement that "stdout and stderr are masked as one stream"
  should read "stdout and stderr are masked independently". Masking matches
  whole registered values and encoded spellings within each stream. Separate
  fragments, including fragments with other text between them, are not masked.
