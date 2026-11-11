### Fixed

- **runner:** Nested child pipelines can start while their run owners wait, even when nesting exceeds the runner's concurrency
- **runner:** Self-hosted evaluators can acquire the sealed pipeline source after a generator's source job finishes
