# Shared runners on a teammate's machine

A shared runner takes repository work for the team: anyone who can push a
branch to one of its `--repos` repositories can run code on it. That code runs
as the OS user the runner service runs as. Give it a user that holds nothing
else.

## What the runner's OS user exposes

Pipeline code can read and use everything that user can:

- SSH keys, cloud credentials (`~/.aws`, `~/.config/gcloud`, `~/.kube`), browser
  profiles and other checkouts in that user's home;
- the runner's own credential, which can report a passing check and claim more
  work;
- the network the machine reaches, including private networks and local
  services.

A runner enrolled into a person's own login exposes that person's whole account.
`sparkwing runners add` warns when a shared runner is enrolled into an account
whose home holds SSH keys, cloud credentials or browser profiles.

## Set it up under a dedicated user

Run this as the teammate, on Linux with systemd or on macOS:

```sh
sparkwing runners add --profile work --runner desk-ci \
  --repos github.com/acme/api --session-stdin --service-user sparkwing-runner
```

Shared runners need a team administrator's session on stdin;
`sparkwing runners add --help` explains where to get one. The command asks for
your sudo password before it mints the runner credential, then:

1. creates the OS user `sparkwing-runner` when it is absent, with no password
   and no login shell. Its home is `/var/lib/sparkwing/sparkwing-runner` on
   Linux or `/private/var/sparkwing/sparkwing-runner` on macOS;
2. writes the runner credential, config and Sparkwing home inside that home,
   readable only by that user. Nothing is written to your own home;
3. installs a system service that runs as that user. On Linux it is
   `sparkwing-runner-sparkwing-runner.service` with `ProtectHome=yes`, so jobs
   see no `/home`, `/root` or `/run/user` at all. On macOS it is the launchd
   daemon `com.sparkwing.runner.sparkwing-runner`.

It refuses an existing user that is root, is your own account, belongs to
`sudo`, `wheel`, `admin`, `docker` or `lxd`, or has its home under `/home`,
`/Users` or `/root`. It also refuses a new user whose name is already taken by a
group, rather than changing that group.

The service does not share your machine's local admission, so
`--max-concurrent` is the only cap on how much it runs. Toolchains installed
under your home, for example by asdf, are out of the service's reach: install
the toolchains its jobs need system-wide. The command lists the PATH entries it
left out.

Remove it with:

```sh
sparkwing runners remove --profile work --service-user sparkwing-runner
```

This stops the service, revokes the credential and prints the command that
deletes the OS user. The token to revoke comes from a record that only root can
change, so a job that deletes the runner's own config cannot keep the runner
alive.

## What a dedicated user does not isolate

- **Jobs from each other.** Jobs on one runner share its user, its credential
  and its caches. A job from one `--repos` repository can claim another's jobs
  and read their secrets. Scope `--repos` to one repository, or to repositories
  that trust each other. `runners add` warns when the scope spans several.
- **The network.** Jobs reach whatever the machine reaches.
- **World-readable files.** On Linux, `ProtectHome=yes` hides every home, but
  world-readable files elsewhere stay readable. On macOS only the user boundary
  applies: anything world-readable on the machine, including in your home, is
  readable by jobs. macOS homes are private by default, as are `~/.ssh` keys.
- **Docker.** Do not add the runner's user to the `docker` group: that group is
  root on the machine. Jobs that need Docker belong on a separate VM.

For stronger isolation, run the runner in a VM. The [threat model](threat-model.md)
covers what any runner trusts.
