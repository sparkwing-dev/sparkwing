# Threat model: another person's branch on your machine

Enrolling a workstation as a runner authorizes pipeline code from the
repositories that controller schedules to compile and execute on it. This page
states what Sparkwing isolates in that case, what it does not, and what an
operator does about the rest. [security.md](security.md) covers transport,
tokens, and secret storage; [local-execution.md](local-execution.md) covers the
local daemon and the laptop boundary.

## The case

Your desktop runs the bundled runner service against a controller your team
shares. A teammate pushes a branch and triggers a run. The controller awards a
node to your runner, your machine fetches that commit, compiles its Go pipeline
code, and runs it. You do not read the branch first, and the award carries no
per-repository consent step.

The same applies to a foreground fleet run (`sparkwing run <pipeline>
--sw-fleet`), where the difference is that your working tree, not a pushed
commit, is what travels.

## What is isolated

**The job body runs in a child process with its own credential.** Every node a
runner claims executes this way. The supervisor keeps the enrollment token, and
the child never sees it: the supervisor starts a broker on `127.0.0.1`, mints a
32-byte random capability that lives as long as that one node, and hands it to
the child on stdin rather than through the environment or argv, where any
process on the machine could read it. `internal/orchestrator` builds both the
broker and the child (`remote_execution_broker.go`, `run_node_remote.go`).

**That capability reaches one node's routes.** The broker proxies the awarded
run's and node's paths -- start, finish, steps, events, heartbeat, artifacts --
and answers everything else with `403`, so the child cannot claim work, renew a
claim, register an executor, or call an administrative route. It also strips
the claim headers the child sends and stamps the supervisor's own, so the child
cannot execute against a claim it was not given. The allow-list is
`allowController` in `internal/orchestrator/remote_execution_broker.go`, and it
is the whole list.

**A secret resolves against the repository of a live claim.** The broker passes
a secret read only when the request names its own run. The controller then
resolves the name against the repository of a run the caller holds a live claim
in, plus the secrets an admin marked shared; a runner token names no
repository of its own, and a principal holding no claim is refused with
`claim_required`. See `readSecretForCaller` in `pkg/controller/secrets.go` and
`GetSecretForRun` in `pkg/store`. Within the claim the grant is the repository,
not the pipeline's declared inputs: code in that run reads any secret name that
repository has.

**The child's environment is rebuilt rather than inherited.** It carries `PATH`,
`HOME`, `TMPDIR`, the locale and TLS-bundle variables, and similar runtime
names, plus whatever the operator lists in `SPARKWING_SUBMIT_ENV_ALLOW`. The
agent token, cache token, artifact store, and claim variables are removed by
name, and any variable whose name or value reads as a credential is dropped even
when the allow-list names it. See `remoteExecutionChildEnvironment` in
`internal/orchestrator/run_node_remote.go` and `internal/envredact`.

**The process tree dies with the node.** The child leads its own process
session. When the node finishes or is cancelled, the supervisor sends TERM, then
KILL, and waits for the session to empty, retrying rather than releasing the
machine's slot while members are alive. Windows starts the body suspended inside
a kill-on-close Job Object and resumes it there, so nothing it spawns exists
outside the job. See `internal/procgroup` and
`internal/orchestrator/run_node_child_process_unix.go`. A node killed without
running any code leaves its step sessions to the ledger sweeps described in
[local-execution.md](local-execution.md).

## What is not isolated

**The OS user.** The pipeline is Go code compiled and executed as the account
the runner service runs under. Every file that account can read, it can read:
your ssh keys, cloud configuration, browser profiles, and the other repositories
on the disk. Sparkwing starts no container, user namespace, or syscall filter
for it. The scoping above keeps the controller's credentials away from that
code; it keeps none of your machine's away.

**Code that leaves its session.** Unix code can call `setsid` and step outside
the session the supervisor kills and the ledger sweeps. Sparkwing treats that as
trusted code running as the agent OS user, not as a containment failure, so a
body that daemonizes outlives the run.

**Network egress.** Sparkwing sets no egress policy for pipeline code. It
reaches whatever the box reaches, your private networks included. It also
reaches the admission daemon's socket, which every process running as that
account may use to queue, inspect, cancel, and drain that account's runs; the
socket's own check is the peer uid (`internal/wingd`).

**The files in a working tree.** A fleet run transmits an immutable snapshot of
every tracked file and every non-ignored untracked file to the helper that wins
a node, so an untracked `.env` beside your code is part of what travels. The
capture rejects submodules, unsafe symlinks, and non-regular files, and it
reports a digest, a file count, and byte totals rather than names
(`cmd/sparkwing/worktree_snapshot.go`); it judges nothing about contents. In the
other direction, the sender's working tree lands on your disk under the runner's
account.

**The contribution cap.** A runner's `--contribution` value caps the headroom it
advertises when it claims (`internal/cluster/headroom.go`), so it bounds how
much work the box accepts, not what an admitted pipeline consumes. The one
OS-level ceiling Sparkwing sets is the machine budget's `enforce` term: on Linux
the admission daemon creates a cgroup v2 with `cpu.max` and `memory.max` and
puts the lease holder in it (`internal/wingd`). `--contribution` does not reach
that mechanism.

**Labels.** Labels are placement, not admission control. A node that requires
no labels matches any runner, and a runner advertises its own set on every
claim (`pkg/store/node_placement.go`), so label terms cannot hold a box to one
repository's work. No repository or pipeline allow-list exists on the claim
path. The boundary is which controller you enroll with: one controller is one
trust domain, so every repository on it reaches every runner whose labels
satisfy a node.

## What to do about it

**Give the runner its own OS account.** This is the control that matters most,
because it is the one that moves the OS user boundary. Create an account for the
runner, install the service under it, and keep your keys, cloud credentials, and
source checkouts out of its reach. Enroll from that account with `sparkwing
cluster runners add`.

**Enroll only against a controller whose repositories you accept.** Every
repository that controller schedules may run on your box. A repository you would
not execute unreviewed needs its own controller, not another token.

**Hold secrets to the repositories that need them.** A secret set on a
repository is readable by any run that repository starts, on any runner that
claims one of its nodes. A shared secret is readable by every repository on the
controller.

**Keep secret-shaped files out of a working tree you start fleet runs from.**
A credential belongs in the controller's secret store, in an ignored path, or
outside the repository. An untracked file that no ignore rule names is in the
snapshot whether or not you meant to commit it, so read `git status` before a
fleet run.

**Bound the machine.** Set a machine budget so a pipeline cannot take the box,
and use its `enforce` term on Linux when you want the ceiling to hold at the OS
level. See
[capping sparkwing's share of the machine](local-execution.md#capping-sparkwings-share-of-the-machine).

**Revoke when a device leaves.** `sparkwing cluster runners remove` stops the
service and revokes that machine's token; the machine keeps a dead credential
rather than a live one.
