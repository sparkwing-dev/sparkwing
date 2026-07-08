---
shape: matrix-fanout
expect: pass
entrypoint: GenMatrix
---
A pipeline that builds once, then fans out to publish the artifact to a
matrix of three targets (linux-amd64, linux-arm64, darwin-arm64) in
parallel. Each publish job depends on the build.
