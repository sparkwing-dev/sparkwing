### Fixed

- Post-deploy scans share one concurrency slot, reuse recent results at the deployed revision, and run full examples and their child pipelines on owned runners. Automatic full scans follow production, and superseded waiting scans stop before build work.
