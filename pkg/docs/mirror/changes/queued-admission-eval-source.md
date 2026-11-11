### Fixed

- **runner:** Jobs waiting for concurrency admission leave runner capacity available for awaited child pipelines, with bounded queued claims and backed-off polling
- **runner:** Self-hosted evaluators can acquire the sealed pipeline source after a generator's source job finishes
