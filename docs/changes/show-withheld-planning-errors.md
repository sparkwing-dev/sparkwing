### Added

- **pipeline:** `sparkwing pipeline describe --show-withheld` prints withheld planning errors
  A pipeline whose Plan fails, or a TypeScript pipeline that does not load,
  used to report only `details withheld: secrets not yet known`, with no way to
  see the cause. The withheld error and the withheld-output notice now name
  `sparkwing pipeline describe --pipeline <name> --show-withheld`. Run it
  locally: it prints the real error and the SDK's output to stderr, masked by
  every secret Sparkwing knows. The failed pipeline's own secret defaults are
  unknown and print unmasked, so keep it out of CI logs. Default output, run
  logs and the describe cache stay withheld.
