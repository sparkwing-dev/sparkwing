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

## Stalled-holder sweep

A *stale* holder is dead; a *stalled* holder is worse: the owner
process is alive (its flock holds), but it stopped making progress --
typically wedged against a locked state database. `sparkwing box-slots
sweep` finds these, and a run waiting for a slot reports them so a
queued run says who it is stuck behind.

The stall signal is the run envelope's mtime, not a process heartbeat.
A live process with frozen database work keeps beating, so a heartbeat
cannot tell "alive and working" from "alive and wedged." The envelope
log at `<runsDir>/<runID>/_envelope.ndjson` moves on run-level events
and stdout tees -- node starts and finishes, teed output lines -- so a
wedged run goes silent. The signal has a known blind spot: a healthy
run inside one long output-quiet node (a buffered subprocess, a silent
computation past the threshold) also goes envelope-quiet and reads as
stalled. The sweep therefore reports a corroborating column alongside
the envelope age: the mtime age of the newest file under the run's
directory. A run still writing node logs or artifacts while its
envelope sits silent is visibly not dead -- check that column, and
raise the stall threshold above your longest expected quiet node,
before trusting `--reap`. The sweep resolves each live holder's
run id from the marker's `run=` line and stats that envelope: silent
longer than the stall threshold (default 30m, overridable via the
`SPARKWING_BOX_SLOT_STALL_TTL` Go-duration environment variable; a
set-but-unparseable value errors loudly) means stalled. A holder with
no `run=` line is judged by the claim time in its filename instead: a
slot held past the threshold without ever starting a run is equally
stalled. An annotated holder whose envelope file is missing falls back
to the same claim-time judgment, with evidence naming the missing
envelope. Like everything else in this contract, the sweep reads only
the filesystem and flock state, so it works while `state.db` is
wedged.

Reporting and killing are separate acts. The sweep itself never
signals anything; `--reap` (or `ReapStalled` in code) climbs a safety
ladder per stalled holder:

1. Verify the marker still exists at the swept path and its filename
   still parses to the swept pid. Any mismatch refuses -- a renamed or
   vanished marker, or a recycled pid, is never signaled.
2. Take a fresh flock probe immediately before SIGTERM. A released
   flock refuses: the owner already exited between the sweep and the
   reap, and its pid may already belong to someone else.
3. SIGTERM the owner and wait a grace window (10s) for it to exit.
4. If the owner still holds its flock after the grace window, take one
   more fresh flock probe immediately before SIGKILL; a released flock
   means the owner exited, otherwise SIGKILL.

The reaper never removes the marker file: the kernel drops the flock
when the owner dies, and admission's stale-file GC (above) deletes the
file on the next acquire. The wait path never reaps on its own --
killing is always an explicit operator act.
