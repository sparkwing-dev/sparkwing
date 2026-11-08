# Controller recovery drill, 2026-11-08

This is implementer evidence for the [backup and recovery runbook](../../../docs/backup-restore.md).
Both SQLite and PostgreSQL passed backup, real binary upgrade, downgrade refusal,
restore and authentication checks. The runbook change adds no product code.

## Revisions and isolation

- Old controller: tag `v0.69.2`, commit
  `70795d1429120a78a41a51ba15684df10daac227`, schema 30.
- Target controller: commit `1ce4624e418ae3da2c25d2cbf8d5af4e00b787ab`, schema 34.
- Both binaries were compiled from their source with `GOWORK=off go build -p 2`
  against `./cmd/sparkwing-controller` and started as real loopback HTTP servers.
- Each drill used private `HOME`, `SPARKWING_HOME`, config, credential and epoch
  directories. PostgreSQL used a dedicated loopback instance and databases.
  No operational database, runner, GitHub installation or payment service was used.
- The Xwing installer supplies a candidate CLI, with verified source and artifact
  identity. It has no controller installer, so the standalone controller used
  the repository's documented Go build path.

## Live controller exercises

The source was a running v0.69.2 controller populated through HTTP with a team,
its first account and API token, a readable encrypted canary and a masked secret.
A fixture helper provisioned a repository-scoped machine credential through the
old store's `ProvisionCapability` API while the controller was stopped. This
tested credential preservation separately from interactive runner enrollment.

At each checkpoint, HTTP health passed, the original API token read team
membership, the canary read decrypted to its original value, and the original
runner credential passed `auth/whoami` and an empty `runners/claim` request.
The team runner inventory route also succeeded. The checks kept values and
bearers out of output.

| Exercise | SQLite | PostgreSQL |
| --- | --- | --- |
| Open populated schema 30 with the old binary | Pass | Pass |
| Back up after stopping the controller | Complete home copy, integrity check `ok` | `pg_dump --format=custom --no-owner --no-privileges`, archive list verified |
| Start target binary against the source database | Migrated to schema 34, HTTP checks passed | Migrated to schema 34, HTTP checks passed |
| Automatic pre-upgrade backup | Schema 30 `.bak` created and opened successfully by the old controller with the original keys | Not applicable |
| Start old binary against the upgraded database | Refused with `newer than this binary` | Refused with `newer than this binary` |
| Restore the pre-upgrade backup separately | Restored home, original encryption key and live external epoch | `pg_restore --no-owner --no-privileges --exit-on-error` into a new database, original key and live epoch |
| Run matching binary's `restore prepare` and `restore acknowledge` | Both passed | Both passed |
| Start old binary against the restored store | Schema 30, original API and runner credentials passed, canary decrypted | Schema 30, original API and runner credentials passed, canary decrypted |
| Upgrade the restored store again | Schema 34, same HTTP checks passed | Schema 34, same HTTP checks passed |

SQLite also created a secret after the backup and confirmed that it was absent
after snapshot recovery. The masked secret's encrypted row survived. The
canary's HTTP read proved decryption with the preserved key. PostgreSQL used
cached PostgreSQL 17.5 server binaries and privately extracted PostgreSQL 17.11
clients, with `pg_service.conf` connections for dump and restore. The server
stopped cleanly after the drill. All controller processes stopped cleanly.

## Repository checks

The Xwing candidate ran the `test` pipeline against `internal/store` and
`cmd/sparkwing-controller`, selecting these test name prefixes:

```text
TestBackupRestoreDrill|TestSQLiteUpgrade|TestRestore|TestSchemaUpgrade|TestSchemaError
```

Run `run-524e76eb3316313033746f2e` passed. This included the SQLite missing-key
negative test, backup pruning after successful upgrades, failed-upgrade backup
retention, recognized and refused migrations, schema error documentation, schedule
holds and acknowledgement, normal credential retention, security sign-out and
lost-epoch refusal. Its PostgreSQL backup subtest skipped because the pipeline
had no PostgreSQL URL. The live PostgreSQL drill above supplied the missing
dialect evidence.

The documentation mirror check, Markdown lint, changelog check and diff
whitespace check passed. No Go files changed, so gofmt had no changed inputs.
The branch comment and sleep checks passed with its merge-base as the baseline.

## Limits and handoff

These were populated isolated installs, not a copy of a customer's database.
The drill proved existing runner authentication and claims, not runner job
execution, a runner binary upgrade, CLI submission or interactive enrollment.
It did not exercise licensed multi-team startup, GitHub App delivery, external
object restore, live billing, or PostgreSQL WAL replay. Schedule restore behavior
was covered by the focused tests, not by waiting for a real cron occurrence.

RDS PITR and GitOps roll-forward were documented against provider documentation
and deployment behavior, without changing Cloud infrastructure. Their live
operator exercises remain separate. No independent verifier ran in this
implementation lane. The lead can use this record for its review and human QA
queue. No new mechanisms or product bug fixes were needed.
