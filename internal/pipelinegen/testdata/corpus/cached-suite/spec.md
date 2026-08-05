---
shape: cached-suite
expect: pass
entrypoint: GenCachedSuite
---
A test pipeline whose suite should not re-execute when nothing it reads
has changed: an unchanged tree replays the previous pass instead of
running the tests again. Key the reuse on the content of the Go sources,
and let a stored result go stale after 24 hours.
