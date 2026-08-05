---
shape: anti-pattern-ref
expect: fail
entrypoint: GenDiscardedRef
---
A build-then-deploy pipeline that creates a typed reference to the
build's output and then never wires it into the deploy job. This is a
deliberately bad generation: the Ref is dead code, so deploy silently
re-derives what build already computed. The linter's unused-ref rule
must reject it.
