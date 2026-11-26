### Changed

- **release:** Read deploy and publication account values from private configuration
  The Cloud deploy pipeline reads its registry, cluster, runtime bases,
  repositories, parameter paths and per-environment object names from a private
  `deploy.yaml` that each deploy job clones from the configuration repository
  the runner's `~/.config/sparkwing-pipelines/env` names, and refuses a missing
  or unknown key. A deploy `--dry-run` reads a local copy from `--deploy-config`.
  The release-index publisher takes its website bucket and CloudFront
  distribution from the `SPARKWING_WEBSITE_BUCKET` and
  `SPARKWING_WEBSITE_DISTRIBUTION` repository variables, `bin/qa-release.sh`
  takes its bucket and signing-seed parameter from `SPARKWING_QA_BUCKET` and
  `SPARKWING_QA_SIGNING_KEY_PARAMETER`, and the owned-SDK pipeline adds owners
  from `SPARKWING_PIPELINES_OWNED_SDK_OWNERS`.
