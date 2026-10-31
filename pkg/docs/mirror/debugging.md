# Inspecting a job

`sparkwing debug env` reads the job's public status, current executor and active
pause. Use a stable run ID or the team's run number. Add `--profile NAME` for a
remote controller. Without a profile, the command reads the local controller.

```sh
sparkwing debug env --run 412 --job build -o pretty
sparkwing debug env --run 412 --job build --profile ci -o json
```

JSON output contains the resolved run ID and number, job status and attempt,
execution location and pause. An explicit `"pause": null` means no active pause.
A failed pause read is an error. Pause diagnostics are masked before the
controller returns them.

Local runs honor `--sw-pause-before JOB`, `--sw-pause-after JOB` and
`--sw-pause-on-failure`. Inspect the persisted pause with `debug env`, then
release it with `debug resume`. A pause after failure preserves the job
error and failed outcome.

Released pauses stay released for that job and reason for the rest of the run.
If a claim expires before the job starts after resuming a pause, recovery can
return the job to pending without pausing again. Restart the debug run to pause
at that boundary again.

Shell attachment requires a running job with a controller-published Kubernetes
target:

```sh
sparkwing debug attach --run 412 --job build --profile ci
```

The controller supplies the pod, namespace and execution container. The command
uses your local `kubectl` credentials. `--namespace NAME` overrides the namespace.
Cloud jobs do not support shell attachment because Cloud admission refuses
`exec` and `attach` connections. Use `debug env` and `runs logs` to inspect a
Cloud job. To investigate a failure interactively, reproduce it locally with
`--sw-pause-on-failure`.

Local processes and runners without a Kubernetes target also report that
attachment is unavailable. Use `debug env` to inspect those jobs.

With `-o json`, attach first emits the execution target, then streams shell output
in `{"kind":"debug_output","text":"..."}` records. Use `-o plain` for raw shell
output. Child stderr remains on stderr. Attachment errors return a failed exit.

`debug resume --run RUN_ID --job JOB` reads the selected pause and asks the same
controller to release it. Use `--profile NAME` for a remote controller. Without a
profile, the command uses the local controller. The controller checks current
`runs.control` authority when it commits the release. Repeating a release leaves
the job's lifecycle and execution fence unchanged. A successful release emits one
JSON receipt, one run/job value in plain mode, or a confirmation in pretty mode.
