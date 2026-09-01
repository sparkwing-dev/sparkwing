# Security Policy

## Supported versions

Sparkwing is pre-1.0 and supports only its latest published release. Security
fixes ship in the next release on the current development line; older releases
do not receive backports. See the [versioning policy](VERSIONING.md) for the
stability contract.

## Report a vulnerability

Report suspected vulnerabilities through
[GitHub's private vulnerability form](https://github.com/sparkwing-dev/sparkwing/security/advisories/new).
Do not open a public issue.

Include:

- the affected Sparkwing version or commit;
- the deployment mode and relevant configuration, with sensitive values removed;
- reproduction steps and the expected security impact; and
- sanitized logs, stack traces, or a suggested fix when available.

Do not submit credentials, tokens, private keys, customer data, or other
secrets. Use placeholders in the initial report; maintainers can coordinate a
safer transfer if sensitive evidence is necessary.
