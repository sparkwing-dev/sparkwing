# Execution attribution migration

Schema 68 adds a defaulted `run_id` column to `github_runner_credentials`.
The store migrates on open. Existing credentials retain an empty run ID, so
their attempts can show the repository but cannot recover the workflow run.
New GitHub Actions credential exchanges record the verified OIDC run ID.

Upgrade every process sharing a runs store before relying on the new execution
history fields. Older binaries do not record the new runner identity, and a
mixed deployment can still produce attempts with incomplete attribution.
