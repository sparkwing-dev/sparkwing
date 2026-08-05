---
shape: anti-pattern-label
expect: fail
entrypoint: GenStrandedLabel
---
A pipeline that pins one job to a runner label and marks another job to
run in-process while also demanding a label. This is a deliberately bad
generation: a blank Requires matches no runner so the job strands
forever, and a label on an Inline job can never be honored. The
linter's runner-label rule must reject it.
