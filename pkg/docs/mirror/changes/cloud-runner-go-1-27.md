### Changed

- **cloud:** Cloud job runners ship Go 1.27.4
  The Cloud runner image's Go toolchain and its prebuilt standard library
  cache move from Go 1.26.10 to Go 1.27.4. A pipeline module that requires
  Go 1.27 no longer downloads a toolchain at the start of each job.
  Modules that require an older Go are unaffected.
