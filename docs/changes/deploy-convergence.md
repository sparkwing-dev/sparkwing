### Fixed

- **deployment:** Skip superseded queued builds before credentials or Cloud smoke, and let staging-verified ancestors of main finish promotion while both production image revisions only advance. Recheck source ancestry immediately before each production GitOps push.
