# Migrating to the next release

## Cloud nodes retire after an idle grace

The Cloud launcher retires a used node once it has seen the node unreserved and
empty for 60 seconds, instead of waiting for the node's run to end. It no longer
sends the `retire_run` query on launcher sync, and the controller no longer
accepts it or returns `run_terminal`.

Deploy the controller and launcher from the same release. An older launcher
against a newer controller has its retirement query refused, logs a retirement
warning at most once a minute, and leaves spent nodes to Karpenter
consolidation until it is upgraded. A newer launcher works against an older
controller, because it sends nothing new.

Jobs that become ready more than about a minute after their run's last node
emptied, such as a deploy after an approval, now boot a fresh node. To keep
nodes longer, pass `sparkwing-runner launch --idle-grace <duration>`; it must be
at least `--poll` plus 30 seconds.

## runners add --trust verified is gone

`sparkwing runners add --trust verified` was removed with no alias. A shared
runner enrolled with `--repos` already takes only work whose repository GitHub
or the controller's own fetch proved, and is now a trusted host whose jobs can
produce a pass that `sw.FromProvenRunAtCommit()` counts. A runner registered
with `--trust verified` keeps claiming only verified default-branch work, so
remove it with `sparkwing runners remove` and enroll it again with `--repos`
and no `--trust`, or `--trust any`.

## Team launcher pods are not fresh hosts

Pods a team's own launcher starts now take the trust of the launcher's
credential. Their jobs mint OIDC tokens with `runner_environment: persistent`,
so a cloud trust policy that requires `fresh` stops accepting them: allow
`persistent` for those subjects, or run the jobs on Sparkwing Cloud. Their
verified runs write the launcher's own cache scope, which other launchers and
Cloud do not restore, unless the team turns on `high_trust`.
