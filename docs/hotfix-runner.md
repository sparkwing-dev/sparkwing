# Hotfix runner

When the team's runners cannot take a run, `sparkwing pipeline trigger --runner here` runs it on the caller's machine without handing that machine the team's secrets. The controller still decides what the run receives, records who started it and why, and confines the machine to that one run.

```sh
git commit -am "fix: roll back the bad migration"
sparkwing pipeline trigger release --profile prod --runner here --reason "INC-42: runners down"
```

The command submits the trigger, builds the checkout's pipeline, hosts the run in this process, follows its logs, and exits on its outcome: 0 for success, 1 for failed or cancelled, 3 when the outcome could not be read.

## How the run is confined

- **One credential, one run, one claim.** With `runner: here` the controller mints a runner credential in the same transaction that writes the trigger, pins the trigger to that credential, and returns the credential once. The pinned trigger is invisible to every other runner's queue claim and refused to their claim by id. The credential claims it by id, only within 10 minutes of the trigger and only while it has never been claimed: a run whose claim lapsed, or that was not claimed in time, is not taken by anyone.
- **Fenced routes.** The credential's principal names its run. Every request it makes passes an allowlist: the run's own run and trigger routes, run creation bound to its own trigger claim, and the claim-bound concurrency, pipeline profile and secret routes. A pipeline profile is readable only for the run's own pipeline while the credential holds the run's claim. The queue claims, other runs, listings and new triggers answer 403.
- **Expiry.** The credential carries the runner scopes only (`nodes.claim`, `triggers.claim`, `runs.state`) and expires six hours after the trigger, the time a runner gives any hosted run. The run's trigger and node claims end with it.
- **Secrets as on any runner.** A secret reaches the run only when its plan declares it, through the trigger's or a node's claim, recorded as a `secret_released` event, and masked in the logs the run uploads. Nothing is written to the local store. A plain `sparkwing run` still reads no controller secret, and the credential cannot read one outside its run's claims.
- **Audit.** The trigger request's audit line names the caller, `runner=here`, the credential's prefix and the reason. The run's event stream carries a `runner_here` event with the same reason, the caller and the credential prefix, written in the transaction that creates the run.

Who may use it: any caller who may start a run (`runs.write`). Such a caller can already mint a machine token or send the working tree to a runner, so `--runner here` grants no secret that was out of reach; it adds a recorded reason. Environment protection rules that require approval before a protected environment's secrets reach a run are not enforced yet.

## Runbook

1. Commit the fix. `--runner here` refuses a checkout with uncommitted tracked changes, because the run records `HEAD`.
2. Run `sparkwing pipeline trigger <pipeline> --profile <profile> --runner here --reason "<incident and why>"`. The reason is one line of at most 200 bytes.
3. Keep the terminal open until the command exits; the run lives in this process. The command claims the run as soon as the trigger is written; if it cannot claim within 10 minutes, the run is cancelled and must be triggered again.
4. To stop, press Ctrl-C once: the run is cancelled and the command exits when the run records it. A second Ctrl-C stops hosting at once.
5. If the machine dies mid-run, the run cannot be claimed again. Cancel it with `sparkwing runs cancel --run <id> --profile <profile>` and trigger again.
6. Review the run's `runner_here` and `secret_released` events afterwards.

## Out of scope

A controller outage is out of scope: `--runner here` needs the controller to mint the credential, release secrets and record the run. Keep production credentials needed to recover the controller itself in the team's secret manager, not in Sparkwing.
