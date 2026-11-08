# Migrating to the next release

No breaking changes so far.

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
