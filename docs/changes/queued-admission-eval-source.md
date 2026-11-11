### Fixed

- **runner:** Jobs in full concurrency groups stay unclaimed so awaited child pipelines can run; admission races use bounded waits and backed-off polling
- **runner:** Self-hosted evaluators can acquire the sealed pipeline source after a generator's source job finishes
