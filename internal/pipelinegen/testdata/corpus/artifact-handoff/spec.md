---
shape: artifact-handoff
expect: pass
entrypoint: GenArtifactHandoff
---
A pipeline where a compile job writes binaries into dist/ and a
separate publish job uploads those exact files. The two jobs may land on
different runners, so the publish job must receive the compiled files in
its own workspace rather than assuming they are already on disk.
