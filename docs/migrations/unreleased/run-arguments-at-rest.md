## Run argument values are sealed in stored runs

**Before:** `runs.request_json`, `runs.spec_json` and
`nodes.expansion_registration` held argument values, pipeline-level declared
defaults and masking registration values in plaintext.

**After:** those values live in a `sealed_arguments` envelope opened with the
secrets key. Job-level `WithArgs` defaults in `runs.plan_json`,
`nodes.spec_json` and `nodes.expansion_body` are not sealed yet. No schema change is involved. Upgrade every controller replica
before it serves new runs: a binary without argument sealing reads a sealed run
with empty argument values and refuses to resume or retry it because its seal
no longer matches. Do not downgrade a store after it has admitted runs with
arguments.

A PostgreSQL controller without a `secrets-key` credential keeps storing run
argument values, including secret defaults, unencrypted, and now warns at
startup. Add the credential to encrypt them; argument documents written while
it was keyless stay plaintext until a rotation seals them.

`sparkwing-controller restore prepare` now reads the serving controller's
`secrets-key` from `--credentials-dir`, or the generated `secrets.key` beside a
SQLite store, so it can cancel scheduled runs whose arguments are sealed. Pass
the same credentials directory the controller uses.

Runs admitted before the upgrade keep their plaintext values. On Cloud and
self-hosted controllers, run `sparkwing operator secrets rotate` to seal them,
then `sparkwing operator secrets rotate --verify` until it reports
`plaintext_run_arguments: 0`. Local stores keep those rows as written. Earlier
copies of the plaintext remain in database free space until vacuum and in
backups taken before the rotation.
