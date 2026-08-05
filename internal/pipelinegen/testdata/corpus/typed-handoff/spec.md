---
shape: typed-handoff
expect: pass
entrypoint: GenTypedHandoff
---
A build-then-deploy pipeline where the build job computes an image tag
and a digest, and the deploy job uses those exact values rather than
re-deriving them. The deploy job must read the build's output as typed
data, not by parsing logs or re-running the build command.
