### Changed

- **cache:** A run on a new branch restores its writer's default-branch memo and dependency caches
  A local, CLI, API or dashboard run reads its own `manual:<writer>:<branch>`
  scope first, then the same writer's scope on the repository's default branch,
  so the first local run on a new branch starts warm from what that writer's
  runs of `main` saved. The default branch comes from `origin/HEAD`. Writes stay
  in the branch's own scope, the records read stay unverified, and no run reads
  another writer's scope. A dependency-cache restore from that scope reports the
  source `writer_default`; a host older than its controller refuses that source
  and runs the job cold.
