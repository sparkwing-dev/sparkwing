### Changed

- **cloud:** Cloud Pods start without downloading the pipeline engine
  The Cloud runner image now ships the signed release files of the engine current SDKs select. Planning and each job's restore verify those files exactly like a download, with the same signatures, digest, platform, embedded version and signed policy checks, instead of downloading the 76 MB executable again in every Pod. Other engine versions, and shipped files that fail verification, are still downloaded.
