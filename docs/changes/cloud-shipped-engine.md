### Changed

- **cloud:** Cloud Pods skip the engine download when the runner image ships the selected release
  Each deploy verifies its release base's signed engine files and ships them read-only in the runner image. When signed policy selects that release, planning and each job's restore verify those files exactly like a download, with the same signatures, digest, platform, embedded version and signed policy checks, instead of downloading the 76 MB executable again in every Pod. Other engine versions, and shipped files that fail verification, are still downloaded.
