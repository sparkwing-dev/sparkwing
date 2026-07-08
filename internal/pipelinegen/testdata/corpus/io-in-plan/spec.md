---
shape: anti-pattern-io
expect: fail
entrypoint: GenIOInPlan
---
A pipeline that reads a VERSION file to decide the build tag. This is a
deliberately bad generation: the file read happens inside Plan(), which
must be pure. The linter's plan-io rule must reject it.
