### Docs

- **secrets:** Clarify the v0.68.0 stream-masking description
  The v0.68.0 statement that "stdout and stderr are masked as one stream"
  should read "stdout and stderr are masked independently". Masking matches
  whole registered values and encoded spellings within each stream. Registered
  values or encoded spellings split across consecutive writes to the same stream
  are masked. Fragments split across streams, or separated by other text within
  one stream, are not masked.

- **secrets:** Document credential-owned team scope for secret API routes
