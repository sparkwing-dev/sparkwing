# Migrating to the next release

## Upgrade the store in place to schema baseline 36

Schema 36 adds direct team foreign keys to GitHub Actions approvals and dispatch
receipts. The existing parent foreign keys already enforce team membership
through their parent rows. This upgrade preserves the store's direct team-key
invariant. Recognized schema 35 stores upgrade automatically on open. SQLite
backs up the old store and rebuilds only these two tables in the upgrade
transaction. PostgreSQL adds constraints in place. Configured secrets, teams,
credentials, runners, installations, schedules and existing Actions rows survive.
Older binaries refuse the upgraded store. Keep the automatic SQLite backup or
a PostgreSQL backup if you need to return to an older binary.

## Upgrade the store in place to schema baseline 35

Schema 35 adds approved GitHub Actions workflow/image pairs and durable dispatch
receipts. Recognized schema 34 stores upgrade in place, preserving configured
secrets, teams, credentials, runners, installations and schedules. Older binaries
refuse the upgraded store. The real GitHub App permission update is a separate
operator action. Existing Actions workers need a matching dispatch receipt to
claim jobs whose repository placement explicitly lists GitHub Actions.

## Windows local runners survive controller replacement

Windows queued work now runs in a detached process under the same user, with
independent stdio and Job Object breakaway. An enclosing Job Object must permit
breakaway; startup reports an error when it forbids it. The controller records
the runner's process birth and reuses a matching live runner after restart, or
starts a successor when it exits. Held work keeps its existing drain behavior.
`sparkwing daemon stop` drains the runner through the local API before stopping
the controller. Windows controllers start on demand; `daemon install` remains
a Linux/macOS command.

## Declare upload allowances in repository config

The `--allow-secret-file` flag is removed. For Cloud source uploads and
working-tree triggers, list literal repository-relative paths under the
`allow_secret_files` key in `.sparkwing/sparkwing.yaml`. These exceptions allow
public credential-shaped fixtures through the file scan. Secret-value scanning
still applies, and repository local restrictions remain binding.
