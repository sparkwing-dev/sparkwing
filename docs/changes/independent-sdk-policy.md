### Changed

- **sdk:** (Breaking) Resolve independent SDK versions through signed protocol acceptance
  `pipeline upgrade --version` selects an SDK release, then the newest permitted engine accepting its protocol. Upgrade output reports both versions. SDK revocations apply to one language and SDK version. Engine updates remain engine-version operations.
  Plain upgrade output reports `sdk=VERSION` and `engine=VERSION` on separate lines. Local runs require a cached signed SDK mapping. See the [migration guide](../migrations/_unreleased.md#independent-sdk-and-engine-policy).
