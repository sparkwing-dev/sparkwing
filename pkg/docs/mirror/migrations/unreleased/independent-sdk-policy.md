## Independent SDK and engine policy

SDK pins select a signed SDK entry and the newest permitted engine accepting its protocol. SDK and engine versions can differ. Refresh the local signed mapping with `sparkwing update` before running a newly published SDK offline.

`pipeline upgrade --version v0.72.0` selects SDK v0.72.0. It does not request engine v0.72.0. Without `--version`, the command selects the latest permitted SDK for the repository's language. Engine updates still select engine versions.

Plain upgrade output changes from one SDK version line to `sdk=VERSION` and `engine=VERSION` on separate lines. Scripts needing structured output should use `sparkwing pipeline upgrade -o json` and read `after.version` for the SDK and `engine.version` for the engine.

Policy publishers retain SDK entries and each engine's accepted protocol declarations in the signed policy. Publish both `sdk-entry-go.json` and `sdk-entry-typescript.json` on the engine release before publishing its index. See [Signed SDK and engine policy](../../engine-policy.md).
