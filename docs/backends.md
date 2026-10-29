# Controller connections

A profile is a named controller connection. It contains a controller URL and
an optional bearer credential in the machine's private `config.yaml`.
It has no state, secrets, cache or log backend settings. Project files cannot
define connection profiles.

```yaml
profiles:
  prod:
    controller:
      url: https://api.example.dev
      token: swk_...
```

Use `sparkwing profiles add --profile prod --controller https://api.example.dev --token-stdin`
to probe and save a connection. The probe checks controller health and reads
one run-list page with the supplied credential. A malformed response or
redirect fails the check. `--no-probe` saves without verification.

Controller URLs need `http://` or `https://` and a host. For example, use
`--controller https://staging.sparkwing.dev`. On connection failure, inspect the
saved URL with `sparkwing profiles show --profile NAME`, repair it with
`sparkwing profiles set --profile NAME --controller URL`, and check your network
and whether the controller is running.

`sparkwing profiles list` lists connection names. `sparkwing profiles show --profile prod`
shows the selected connection with its token redacted. `sparkwing profiles set --profile prod --token-stdin`
replaces a credential without putting it in shell history.

Profile edits preserve other machine settings. Adding a connection rechecks
name collisions after probing. Removing one revokes its member credential
before removing the connection, unless `--keep-token` is set. If the connection
changes while revocation is in progress, removal fails and retains the changed
connection. Runner credentials cannot be revoked by profile removal.

Select a connection with `--profile NAME` on controller commands, such as
`sparkwing runs list --profile prod`. Without a profile, observations use the
local controller. No environment variable selects a default profile.
`sparkwing run` executes locally and accepts no `--profile`.

## Pinning a pipeline to a profile

The pipeline's `profile:` setting selects a controller connection for that
pipeline. It does not define the connection or grant access to its controller.

```yaml
pipelines:
  - name: release-prod
    entrypoint: Release
    profile: prod
```

Use `sparkwing pipeline trigger release-prod --profile prod` for that pipeline.
Local runs refuse a controller-pinned pipeline. The controller still decides
who can submit a run and which credentials it can use.

See [Machine settings](machine-config.md) for the file format and
[Config reference](config-reference.md) for project settings.
