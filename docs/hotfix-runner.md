# Host a controller run on this machine

`pipeline trigger --runner here` reserves one controller run for the caller's machine. The controller keeps the run history, authorization and results. The shared run host executes the sealed source and plan on that machine.

Initial trigger intake and native shared hosting remain incomplete in this build. The retry and resume adapters use the final contracts below. A missing native host reports `unavailable` before it claims work.

```sh
sparkwing pipeline trigger release --profile prod --runner here --reason "runners unavailable"
```

## Authority and recovery

Intake requires the caller's live `runs.write` authority. It returns a private bearer once, bound to the requesting authority and the reserved run. The CLI retains that bearer in an owner-only file under the Sparkwing configuration directory's `here/` directory. The file is bound to the controller URL and stable run ID. A local controller binding also includes its absolute API socket path, so another Sparkwing home cannot reuse that authority. It contains no execution environment or run spec.

The first claim must meet the controller's ten-minute `claim_by` window. After a claim lapses, the same bearer can reclaim the same live run beyond that initial window. The reservation and its original expiry stay unchanged. The controller rechecks the run, requester, ancestry and current `runs.write` authority. Other runners cannot take the reservation.

If intake succeeds but its first response is lost, an idempotent replay returns the existing run without another bearer. A retained bearer permits resume. Without it, make a fresh trigger request. The abandoned reservation expires under its original policy. The client never retries an award-capable claim automatically.

## Resume a run

```sh
sparkwing pipeline trigger --resume RUN_ID --profile prod
```

A stable run ID or team run number selects the existing run. Resume resolves its canonical ID, loads only that controller/run's retained bearer and makes one named owner claim. It creates no run. The award supplies the original sealed request, current fence, granted runner identity and any accepted plan. A retry's new run ID remains separate from the original request's sealed identity.

Resume accepts no new pipeline, arguments, reason, ref, working tree or detach option. It does not reread checkout or machine settings to replace sealed values. Source acquisition and execution use the awarded source and accepted plan.

JSON success output contains `run_id` and `outcome`. Plain output contains the run ID. Private bearers, input values and seal hashes stay out of output and diagnostics. Incomplete hosting exits 3. A recorded failed or cancelled run exits 1. A recorded successful run exits 0.

`runs retry` also retains a here retry's one-time bearer before attempting hosting. An unavailable host leaves that private record intact and reports the allocated run in its JSON failure receipt.

## Secrets and lifetime

Here authority grants access only to its bound run and declared data. The controller applies the same secret preflight and release checks as other placements. Pool-restricted secrets remain unavailable to a here runner. Descendant authority stays under the requester's ancestry.

The controller owns execution deadlines and credential expiry. A remote owner's default execution budget is six hours, subject to its sealed operator policy and parent lifetime. Resume never extends those bounds.

A controller outage prevents claims, secret release and result recording. Credentials used to recover the controller belong in the team's separate secret manager.
