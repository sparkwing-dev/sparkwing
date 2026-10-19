# Threat model: another person's branch on your machine

Enrolling a workstation authorizes accepted repository code to run as the
runner's OS account. A claim limits controller API authority. It does not turn
that account into a sandbox.

This page describes the rewrite contract. Native host, controller, source and
process-cleanup integrations still require acceptance evidence. See
[security](security.md) and [rewrite integration](architecture.md).

## The case

A teammate triggers a branch on a controller your runner polls. The controller
checks the runner's repository restrictions, granted reservations, requirements
and capacity. If eligible, it awards the sealed commit to the runner. Planning,
job bodies and host-evaluated closures execute that branch's code. Trust review
must include all of them.

## Controller authority

The runner's enrollment credential claims work. An award carries the narrower
unit credential and a server-owned binding. The shared host retains that
credential. An SDK process receives its process-lifetime node-host capability
over the private protocol pipe. The process cannot claim work, renew another
holder, impersonate a team or call administrative routes through that capability.

Protocol v2 negotiates its version once in hello. Service calls use the negotiated
job routes. There is no per-request version header or `426` negotiation path.
Every call checks its capability, route, current unit and liveness. The host
forwards controller operations under the corresponding claim authority.

Secret release requires an accepted declaration and current bound authority.
A declaration does not widen the secret's pipeline/shared or pool scope. An
authorized branch can declare and use its permitted secrets, so everyone who
can change and run that code must be trusted with those secrets.

Source credentials require a live authorized claim. Naming a private repository
grants no fetch access. Team Git credentials additionally require Cloud work or a
shared runner of that team, never a personal machine. Owner Git credentials stay
subject to the runner's repository allow-list. Releases are audited.

## Process environment and cleanup

Every spawn uses the shared environment policy. Local runs capture values at
submission. Children inherit that snapshot, and schedules capture theirs at
installation. Remote input uses the minimal/network/CA base and explicitly
allowed names. A daemon's current environment never replaces the sealed snapshot.

Credential-shaped names remain excluded even when allowed. Proxy credentials
are the passed and masked exception. Controller and node-host capabilities,
bootstrap values and protocol handles never travel in environment variables.
Startup settings are projected before process creation. Body and cleanup receive
the same complete attempt environment.

Unix cleanup owns process sessions. A process can escape that accounting by
creating another session. Windows uses kill-on-close Job Objects. These cleanup
mechanisms reclaim ordinary descendants. They are not an isolation boundary
against malicious code already running as the account.

## What the account can reach

Pipeline code may read every file and credential its OS user can read, including
the runner's private credential file, SSH keys, cloud profiles, browser data and
other checkouts. A filtered child environment does not remove those permissions.
Code can use the machine's network access, including private networks and local
services. Same-account code is also inside the local controller's peer boundary.

A working-tree submission transfers repository contents to the execution machine.
Keep credentials outside the snapshot and review secret-shaped files before
using an explicit allowed-file exception. Final source-bundle upload and intake
still require native bindings. See [remote execution](local-execution.md#remote-execution).

Free labels steer placement. They grant no repository access or reserved-pool
membership. Repository restrictions and granted reservations filter claims
before preferences rank work. A controller connection alone does not override
those restrictions. A self-asserted `pool=production` label is not a reservation.

The contribution preference controls advertised headroom. It does not constrain
what accepted code can consume at the OS level. Machine admission and any
available cgroup enforcement are separate controls. See
[machine budgets](local-execution.md#capping-sparkwings-share-of-the-machine).

## Cloud boundary

Every Cloud plan, eval and work Job runs as root with privileged DinD. One Job
per team-dedicated node is mandatory. Neither another team nor another Job of
the same team shares that node while it is reserved.

The launcher stays outside pipeline execution. A Job receives a single-use
bootstrap file, rather than a runner root or launcher credential. Node exclusivity,
network enforcement, protected credentials and full teardown require deployment
proof. A rendered pod specification or label does not prove isolation.

## Operator actions

- Run the service under a dedicated OS account or isolated VM/container. Keep
  unrelated credentials and personal data outside its reach.
- Enroll only repositories whose accepted code may act as that account. Check
  repository restrictions and granted reservations separately from free labels.
- Scope secrets to the pipelines and pools that need them. Code authorized to
  use a secret can disclose it despite output masking.
- Review files before a working-tree submission. Keep credentials in ignored
  paths or outside the checkout.
- Use machine admission and available OS resource enforcement to bound load.
  Observe missing headroom as unavailable data.
- Remove a departed device with `sparkwing runners remove`. Revoke credentials
  through their issuing authority. Already issued signed URLs, GitHub credentials
  and OIDC tokens retain their own expiry.
