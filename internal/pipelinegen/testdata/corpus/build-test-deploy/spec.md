---
shape: linear-gate
expect: pass
entrypoint: GenBuildTestDeploy
---
A three-stage CI pipeline: build, then test, then deploy, each
depending on the previous stage. Every stage runs a shell command.
The classic build -> test -> deploy gate.
