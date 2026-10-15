# Credential management

The typed client separates team and deployment credential management. Final
controller provisioning and native runner configuration remain integration
requirements.

Save an existing controller credential without putting it in argv:

```sh
sparkwing profiles add --profile prod \
    --controller https://sparkwing.example.com --token-stdin
```

Removing a profile revokes a confirmed member credential unless `--keep-token`
is explicit. Runner and deployment credentials require `--keep-token` because
removing a saved connection must not revoke the machine that uses it.

Team credential commands use the team token namespace:

```sh
sparkwing tokens list --profile prod
sparkwing tokens list --include-revoked --profile prod
sparkwing tokens list --token swk_example --profile prod
sparkwing tokens create --type member --principal alice --scope runs.read,logs.read --profile prod
sparkwing tokens rotate --token swk_example --grace 48h --profile prod
sparkwing tokens revoke --token swk_example --profile prod
```

Deployment credentials use a separate namespace and need its deployment grant:

```sh
sparkwing operator tokens list --profile prod
sparkwing operator tokens create --type service --principal launcher --scope claims.launch --profile prod
```

List JSON emits one metadata object per line. Plain lists emit public prefixes.
Creation and rotation JSON contain the new bearer and metadata. Plain creation
and rotation print the new bearer once. Treat either output as a secret.
Revocation requires the server to confirm the selected prefix was revoked.
Failures keep their structured error on stderr.

Omitting creation TTL selects the server default. Explicit `--ttl 0` requests
no expiry, subject to issuer policy. Rotation with zero or omitted TTL keeps the
old remaining lifetime. Grace defaults to 24 hours. TTL and grace use nonnegative
whole seconds. A credential mutation keeps one idempotency key and the same body
across transport retries.

Runner enrollment uses the same typed team token manager. Its JSON result
contains the runner name, profile, config path, credential metadata and service
result. It never prints the bearer. Plain prints the runner name. Removal checks
the enrolled controller, optional runner name and explicit credential kind
before stopping its service and revoking the credential. A principal spelled
`agent:NAME` does not establish runner kind.

The local runner file and service installer still need their final machine
schema and native runtime integration. `--reserved-for` needs the team-admin
provisioning request contract. It fails as `policy_missing` before enrollment.

