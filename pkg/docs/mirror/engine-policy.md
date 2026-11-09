# Signed SDK and engine policy

`versions.json` and its detached Ed25519 signature authorize engines and SDKs independently. The signed body retains engine records in `versions` and SDK entries in `sdks`.

```json
{
  "sdks": [
    {"language": "go", "version": "v0.1.0", "protocol": 2, "source_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
    {"language": "typescript", "version": "v0.72.0", "protocol": 2, "source_digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
  ],
  "versions": [
    {"version": "v0.80.0", "published_at": "2026-11-09T00:00:00Z", "support_until": "2027-11-09T00:00:00Z", "accepted_sdk_protocols": [2]}
  ]
}
```

The example shows the mapping fields. The full policy also carries `format`, `published_at`, `latest`, `minimum`, and `revoked`. `latest`, `minimum`, and top-level `revoked` describe engine releases. An SDK entry's optional `revoked` object uses the same restriction fields: `version`, `reason`, `security`, `announced_at`, and `effective_at`. Its restriction applies only to that language and SDK version. Versions use canonical `v` prefixes in the policy, including TypeScript entries.

`pipeline upgrade` first selects the highest permitted SDK version for the repository's language. `--version` selects an explicit SDK version. It then selects the highest permitted engine declaring acceptance of the SDK's protocol. An engine release can leave every SDK entry unchanged. The accepted run retains the SDK pin and engine version separately. Resuming that run checks its recorded engine and SDK policy rather than selecting a newer engine. Engine binary updates continue to use engine release versions.

Local execution reads the cached signed mapping without fetching policy. An unlisted SDK cannot select an engine. Run `sparkwing update` to refresh the policy cache. A verified engine binary alone does not authorize an SDK-to-engine mapping.

## Publication

Each SDK publication decision retains `sdk-entry-go.json` or `sdk-entry-typescript.json` on the immutable engine release. Each file contains a JSON array of SDK entries. An unchanged source digest retains the prior entry verbatim. A changed digest requires a separately chosen SDK version and successful SDK publication before retaining the entry.

Authenticate the previous policy with `go run ./cmd/verify-release --index-out DIR --verify` before reading `DIR/versions.json`. Publication passes the retained arrays to `verify-release --sdk-entries FILE`. The signer preserves previous entries and refuses a changed protocol or digest for an existing language/version pair. Release artifacts cannot set revocations. Policy operators sign revocations through the policy itself.

`bin/publish-release-index.sh` downloads both SDK entry assets from the selected release tag and merges their arrays. Index-only retries consume those same assets. `--accepted-sdk-protocols 2` declares the required engine's accepted protocol set. Pass a comma-separated list when an engine implements multiple protocols. Historical engine records retain their original declarations.
