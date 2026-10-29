---
shape: anti-pattern-guards
expect: fail
entrypoint: GenContradictoryGuards
guard-when: where:local, where:controller
guard-unless: where:controller
---
A deploy pipeline gated to run only against a controller placement, while
also requiring a local placement and rejecting the controller placement it
requires. The tokens are mutually exclusive, so the pipeline can never dispatch. The Go source is
idiomatic; the contradiction lives entirely in the guards block. The
linter's guard-misuse rule must reject it.
