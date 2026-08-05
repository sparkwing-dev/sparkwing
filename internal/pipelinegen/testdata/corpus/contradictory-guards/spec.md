---
shape: anti-pattern-guards
expect: fail
entrypoint: GenContradictoryGuards
guard-require: profile:local, profile:controller
guard-reject: profile:controller
---
A deploy pipeline gated to run only against a controller profile, while
also requiring a local profile and rejecting the controller profile it
requires. This is a deliberately bad generation: the tokens are
mutually exclusive, so the pipeline can never dispatch. The Go source is
idiomatic; the contradiction lives entirely in the guards block. The
linter's guard-misuse rule must reject it.
