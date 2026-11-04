# Migrating to the next release

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
