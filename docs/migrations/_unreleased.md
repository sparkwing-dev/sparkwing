# Migrating to the next release

## Local run retries

`runs retry` refuses local runs because a new attempt cannot recover the
original execution environment. Captured submission environments are deleted
when execution starts.
Queued local retries without an execution environment also fail before dispatch.

Previously:

```sh
sparkwing runs retry --failed --run run-fictional
```

Submit the pipeline again from its checkout, with the intended environment and
original inputs:

```sh
sparkwing run build --sw-detached
```

This creates a new run; it does not resume the failed run's node outcomes.
Controller-backed retries keep using their configured execution context:

```sh
sparkwing runs retry --failed --run run-fictional --profile prod
```
