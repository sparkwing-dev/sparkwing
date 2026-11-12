## Group queue timeouts and store schema 38

A positive concurrency group `QueueTimeout` includes the job's initial wait
before a runner award. It begins when an eligible ready job first encounters a
full group. Claim polling or reconciliation fails an expired job with
`queue_timeout` and no execution attempt. A later award or controller restart
does not restart this clock. An already granted admission remains granted when
replayed after the deadline.

Store schema 38 adds a private nullable group-wait timestamp. The controller
upgrades a schema 37 store automatically; SQLite creates its usual backup first.
Existing rows begin with no recorded pre-award wait. Already queued awards keep
their recorded admission start. Older binaries refuse the upgraded store; use a
matching binary or restore its pre-upgrade backup into a separate store.

No pipeline configuration change is required. Deploy the controller and update
the runner to use the claim-selection and admission fixes together.
