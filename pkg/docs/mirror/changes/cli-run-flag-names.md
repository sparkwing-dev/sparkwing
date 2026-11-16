### Changed

- **cli (Breaking):** `run` and `pipeline plan` flags drop their prefix and match `pipeline trigger`
  Use `--detach`, `--dry-run`, `--ref`, `--pipeline-ref`, `--route`,
  `--allow-risk`, `--only`, `--start-at`, `--stop-at` and the other plain
  names. An old prefixed spelling is refused with a usage error that names its
  replacement. A pipeline pinned to an older engine refuses the new names until
  `sparkwing pipeline upgrade` moves its pin; the refusal says so. See the [migration guide](docs/migrations/_unreleased.md#run-and-plan-flags-lose-their-prefix)
