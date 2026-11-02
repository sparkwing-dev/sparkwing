# GitHub installation token audit

Audited 2026-11-02. GitHub installation tokens remain opaque through minting,
encrypted storage, HTTP release and source checkout. Stored HTTPS credentials
accept 4,096 printable ASCII characters without spaces. Minted tokens and the
literal log masker do not impose that storage-input bound.

GitHub's [format announcement](https://github.blog/changelog/2026-04-24-notice-about-upcoming-new-format-for-github-app-installation-tokens/)
describes variable-length `ghs_APPID_JWT` tokens of about 520 characters.
Its [override announcement](https://github.blog/changelog/2026-05-15-github-app-installation-tokens-per-request-override-header/)
documents `X-GitHub-Stateless-S2S-Token: enabled` on installation-token creation.
The [rollout completion announcement](https://github.blog/changelog/2026-10-02-stateless-github-app-installation-tokens-rolled-out/)
says the header stops taking effect on November 30, 2026. The client sends no
override header. Format tests supply long tokens directly, and App-backed suites
use a fake that mints the new format by default.

## Format assumptions

Line references describe the audited source tree.

| Location | Assumption and disposition |
| --- | --- |
| `internal/controller/git_credentials_team.go:236` | Stored credential input is nonempty, at most 4,096 bytes, and printable ASCII without spaces. No prefix, JWT or fixed-length validation. |
| `web/src/lib/gitCredentials.ts:106`, `:124` | Console uses the same printable character set and 4,096-character bound. |
| `internal/source/credential.go:38` | HTTPS credentials reject empty secrets and NUL/CR/LF. No token shape or length check. |
| `internal/envredact/envredact.go:605` | Credential-file scanning has a 16-byte minimum. A `ghs_` prefix recognizes installation tokens before the generic character-run heuristic, which resets on dots, underscores and hyphens. |
| `pkg/docs/scrub_test.go:37` | Embedded-doc scanner recognizes `ghs_` followed by at least 20 letters, digits, dots, underscores or hyphens. Other GitHub token prefixes retain their separate alphanumeric pattern. |
| `.gitleaks.toml:6`, `:11` | The installation-token rule accepts 36 or more letters, digits, dots, underscores or hyphens after `ghs_`. The fixed 36-character `ghu_` rule describes user tokens, outside this format change. |
| `internal/githubapp/githubapptest/githubapptest.go`: `handleMint` | The local fake mints `ghs_<appid>_<header>.<payload>.<sig>` tokens of about 520 bytes with random base64url segments. Every App-backed suite exercises this shape. Dedicated token-format tests also supply long tokens directly. |

## Transport, storage and masking

| Location | Result |
| --- | --- |
| `internal/githubapp/githubapp.go:285`, `:418`, `:846`, `:874` | Token is a Go string. Minting JSON and outgoing authorization headers preserve it. The GitHub response-body allowance is 1 MiB. |
| `internal/controller/source_controller.go:67`, `:130`, `:180` | Source-release JSON carries the full string after bound decryption. No fixed-size token buffer. |
| `internal/controller/client/source_access.go:40`, `internal/controller/client/engine.go:109` | Source HTTP client decodes a string from the complete response body. No token regex or size limit. |
| `internal/controller/credential_mutations.go:35`, `internal/controller/handlers.go:239` | Credential writes have an 8 MiB JSON-body allowance, above the stored-token bound. |
| `internal/store/schema.sql:1019`, `internal/secrets/aead.go` | Encrypted credential occupies `TEXT`, with no `VARCHAR` or token-length constraint. Encryption uses dynamically sized byte slices. |
| `internal/secrets/mask.go:39`, `internal/source/credential.go:76` | Masking registers literal values and encoded spellings, without inferring token format. Git authentication registers both the full secret and the Basic header before executing git. Streaming retains matching prefixes across chunks. |
| `internal/cluster/production_runtime.go:135`, `:155`, `:256` | Runner and cloud bootstrap share source access and authentication. Actions supplies its own `GITHUB_TOKEN` as an opaque string. Launcher provisioning does not parse installation tokens. |
| `sparkwing/secrets.go`, `sdk/typescript/src/routes.ts:81` | SDK secret values are strings. Source credentials remain host-owned rather than crossing an SDK-specific token validator. |

Repository-wide searches found no installation-token fixed buffers, database
column limits, explicit HTTP header-size limits or proxy header-size overrides.
Default HTTP header handling accommodates a 4,096-byte token and its Basic
authorization encoding. No schema or wire-format change is needed.

## Regression evidence

Tests cover 40-, 520- and 4,096-byte minting, authorization, value and streaming
masking, and gitleaks detection. Storage and source HTTP tests cover 520 and
4,096 bytes. The real git credential-helper test covers 520 bytes and verifies
raw-secret and Basic-header masking. The Actions warm-runtime test carries a
520-byte token with two dots from the environment to source access. Scanner
and console tests cover the expanded character set. All credentials and
servers in these tests are synthetic and local.
