# Box-slot lock file contract

The host box-slot semaphore (see [local execution](local-execution.md))
is implemented entirely with lock files and OS file locks -- no state
database involvement. That makes the on-disk layout an operational
interface: `sparkwing box-slots list` and `release` read it, and so can
you with `ls` and `cat` while everything else is wedged. This page is
the contract for that layout.

**Contract version: 1.** The layout below is a stable interface. Any
change to the directory, filename shape, or file contents bumps the
version noted here and lands with a CHANGELOG entry, so tooling that
parses these files has a single place to watch.

## Directory

All semaphore state lives in one directory under the sparkwing state
root:

```
<stateDir>/box-slots/
```

Alongside the per-process marker files this contract describes, the
directory holds `coord.lock` (the admission mutex) and `cap.control`
(the live cap written by `box-slots set`).

## Filename shape

Each process that holds or waits for a slot owns one marker file:

```
holder-pid<PID>-<unixNano>-<seq>.lock
waiter-pid<PID>-<unixNano>-<seq>.lock
```

- `holder-` marks a process that holds a slot; `waiter-` marks one
  blocked waiting for a slot.
- `<PID>` is the owning process id.
- `<unixNano>` is the claim time as nanoseconds since the Unix epoch.
- `<seq>` is a per-process counter that keeps names unique when one
  process creates several markers.

The filename is metadata; the flock (below) is the authority on
whether the owner is alive.

## File contents

Line 1 is written at admission:

```
pid=<pid> start=<rfc3339>
```

Once the orchestrator mints the run id -- which happens after
admission -- it appends one line:

```
run=<runID>
```

so a holder's lock file names the run occupying the slot. A process
that runs several pipelines under one slot appends one `run=` line per
run; readers take the last. A holder that dies between admission and
run start leaves a file with no `run=` line.

## Flock semantics

The owner keeps an exclusive `flock` on its marker file for its
lifetime. The kernel releases the flock when the process exits --
including crash and SIGKILL -- so a lock file whose flock can be taken
non-blockingly is *stale*: its owner is gone. That non-blocking probe
is the liveness test `box-slots list` reports as `live`/`stale`, and
the reason a stale file never blocks a slot for long.

## Stale-file cleanup

Admission garbage-collects: each acquire attempt probes every
`holder-` file under the coordination lock and deletes the stale ones
before counting, and sweeps stale `waiter-` markers opportunistically.
A stale file therefore disappears on the next run's admission; to
remove one immediately (or to evict a live holder), use
`sparkwing box-slots release`.
