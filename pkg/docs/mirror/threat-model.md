# Threat model: another person's branch on your machine

Adding a workstation as a runner authorizes pipeline code from the
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

## What is isolated

**The job body runs in a child process with its own credential.** Every node a
runner claims executes this way. The runner token stays in the runner process,
which claims and renews the node; the supervisor works on the node's claim
token, which reaches only that claim's run and node and dies with the claim,
and the child sees neither: the supervisor starts a broker on `127.0.0.1`, mints a
32-byte random capability that lives as long as that one node, and hands it to
the child on stdin rather than through the environment or argv, where any
process on the machine could read it. `internal/orchestrator` builds both the
broker and the child (`remote_execution_broker.go`, `run_node_remote.go`).

**That capability reaches one node's routes.** The broker proxies the awarded
run's and node's paths -- start, finish, steps, events, heartbeat, artifacts. It
reads node metadata and outputs within that run for dependencies, and proxies
concurrency acquire, observation, renewal, waiter resolution, waiter
cancellation, and release
for that node's holder. The controller resolves concurrency keys in the
claim's team, so the same key in another team remains separate. The
broker answers everything else with `403`, so the child cannot claim work, renew a
claim, or call an administrative route. It also strips
the claim headers the child sends and stamps the supervisor's own, so the child
cannot execute against a claim it was not given. The allow-list is
`allowController` in `internal/orchestrator/remote_execution_broker.go`, and it
is the whole list.

**A secret resolves through the node's claim and its plan.** The broker passes
a secret read only when the request names its own run, and forwards it with
the node's claim token. The controller answers only a name the run's plan
declares, resolved against the run's pipeline plus the secrets an admin marked
shared, and records the release as a `secret_released` event; an undeclared
name is refused with `secret_undeclared`. The runner's own token is refused
with `claim_required`, and only an operator or team-admin credential reads a
row by name. See `handleClaimSecret` in `pkg/controller/claim_run.go` and
`ReleaseClaimSecret` in `pkg/store`. The plan is the pipeline's own code, so
the grant is what that code declares: a branch that adds a declaration reads
that secret of its pipeline.

**A source credential resolves through the claim, before the child starts.**
The supervisor asks `POST /api/v1/runs/{id}/source-credential` with the node's
claim token and fetches the run's source before it starts the child, and the
broker does not proxy that route. The controller answers only a claim token of
the run, a few times per claim and never once the attempt has started, and the
GitHub App token it mints reads the repository the App's webhook recorded on
the trigger, or a repository the run names by hand only when the team
subscribed it. A run that names another private repository of the
installation by hand therefore gets no token for it. A team's stored git credential
for the host goes only to a machine its owner opted in. See
`handleRunSourceCredential` in `pkg/controller/claim_source.go`.

**The child's environment is rebuilt rather than inherited.** It carries `PATH`,
`HOME`, `TMPDIR`, the locale and TLS-bundle variables, and similar runtime
names, plus whatever the operator lists in `run.submit_env_allow`. The
agent token, cache grant, artifact store, and claim variables are removed by
name, and any variable whose name or value reads as a credential is dropped even
when the allow-list names it. See `remoteExecutionChildEnvironment` in
`internal/orchestrator/run_node_remote.go` and `internal/envredact`.

**The process tree dies with the node.** The child leads its own process
session. When the node finishes or is cancelled, the supervisor sends TERM, then
KILL, and waits for the session to empty, retrying rather than releasing the
machine's slot while members are alive
(`internal/orchestrator/run_node_child_process_unix.go`, `internal/procgroup`).
Windows starts the body suspended inside a kill-on-close Job Object and resumes
it there, so nothing it spawns exists outside the job
(`internal/procgroup/job_windows.go`). A node killed without
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

**The files in a working tree.** A `sparkwing pipeline trigger --working-tree`
run transmits an immutable snapshot of every tracked file and every non-ignored
untracked file to the runner that claims it, so an untracked `.env` beside your
code is part of what travels unless the secret-shaped file check refuses it
(see [remote execution](local-execution.md#remote-execution)). The capture
rejects submodules, unsafe symlinks, and non-regular files, and it reports a
digest, a file count, and byte totals rather than names
(`cmd/sparkwing/worktree_snapshot.go`). In the other direction, the sender's working tree lands on your disk under the runner's
account.

**A trigger consumer's child.** A pool started with `--also-claim-triggers`
compiles the triggering branch and runs its binary, which plans and drives the
whole run. It holds a capability for a loopback broker in the runner process
rather than the runner's token, and the broker admits only its run's routes
(`internal/orchestrator/trigger_broker.go`), but that is a run's worth of
authority, wider than one node's, and the child-environment filtering above
does not apply to it. A Kubernetes or warm trigger child creates Jobs, each
carrying its node's claim token. `sparkwing cluster runners add` never sets
that flag, so a desktop added by the installer does not take that path.

**The contribution cap.** A runner's `--contribution` value caps the headroom it
advertises when it claims (`internal/cluster/headroom.go`), so it bounds how
much work the box accepts, not what an admitted pipeline consumes. The one
OS-level ceiling Sparkwing sets is the machine budget's `enforce` term: on Linux
the admission daemon creates a cgroup v2 with `cpu.max` and `memory.max` and
puts the lease holder in it (`internal/wingd`). `--contribution` does not reach
that mechanism.

**Labels.** Labels are placement, not admission control. A node that requires
no labels matches any runner, and a runner advertises its own set on every
claim (`labelsSatisfied` and the claim scan in `pkg/store/store.go`), so label
terms cannot hold a box to one repository's work. No repository or pipeline
allow-list exists on the claim path. The boundary is which controller you
add the runner to: one controller is one trust domain, so every repository on
it reaches every runner whose labels satisfy any unsealed node.

A sealed node is the narrower case. The claim scan skips nodes that are sealed
or pinned to a coordinator or executor location (`pkg/store/store.go`), so a
pool runner never sees that work.

## What to do about it

**Give the runner its own OS account.** This is the control that matters most,
because it is the one that moves the OS user boundary. Create an account for the
runner, install the service under it, and keep your keys, cloud credentials, and
source checkouts out of its reach. Add the runner from that account with
`sparkwing cluster runners add`.

**Add runners only to a controller whose repositories you accept.** Every
repository that controller schedules may run on your box. A repository you would
not execute unreviewed needs its own controller, not another token.

**Hold secrets to the repositories that need them.** A secret set on a
repository is readable by any run that repository starts, on any runner that
claims one of its nodes. A shared secret is readable by every repository on the
controller.

**Keep secret-shaped files out of a working tree you send with
`--working-tree`.** A credential belongs in the controller's secret store, in
an ignored path, or outside the repository. An untracked file that no ignore
rule names is in the snapshot whether or not you meant to commit it, so read
`git status` before such a run.

**Bound the machine.** Set a machine budget so a pipeline cannot take the box,
and use its `enforce` term on Linux when you want the ceiling to hold at the OS
level. See
[capping sparkwing's share of the machine](local-execution.md#capping-sparkwings-share-of-the-machine).

**Revoke when a device leaves.** `sparkwing cluster runners remove` stops the
service and revokes that machine's token; the machine keeps a dead credential
rather than a live one.
