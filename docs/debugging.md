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

To open a shell in a running Kubernetes job, use:

```sh
sparkwing debug attach --run 412 --job build --profile ci
```

The controller supplies the pod, namespace and execution container. The command
uses your local `kubectl` credentials. `--namespace NAME` overrides the namespace.
Local processes and runners without a Kubernetes target report that attachment
is unavailable. Use `debug env` to inspect those jobs.

With `-o json`, attach first emits the execution target, then streams shell output
in `{"kind":"debug_output","text":"..."}` records. Use `-o plain` for raw shell
output. Child stderr remains on stderr. Attachment errors return a failed exit.
