### Fixed

- **cli + web:** Local runs record the checked-out branch, so Home lists their failures and recoveries
  A `sparkwing run` from a working tree records its branch, as `--ref`, uploaded
  and cloud-routed runs already did. Home's "Recently failed pipelines" and
  "Recently recovered pipelines" now include local runs, `runs list --branch`
  matches them, the SDK's `Git().Branch` reports the branch, and `git:branch=NAME`
  guards match the checked-out head as documented. A detached HEAD records no
  branch. Memo and dependency-cache records from local runs now carry the branch
  in their scope, `manual:<writer>:<branch>`, as the caching guide describes, so
  the first run on another branch starts cold. Home also treats a
  `refs/heads/main` push or schedule run as a default-branch run.
