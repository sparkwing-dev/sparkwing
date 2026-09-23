# OIDC tokens for cloud roles

A controller with a signing key is an OpenID Connect issuer. A run asks it for a short-lived ID token that names the run's team, pipeline, trigger and ref, and exchanges that token with AWS, Google Cloud, Azure or Vault for credentials. No cloud key is stored in Sparkwing secrets, and a cloud role trusts only the pipelines its trust policy names.

The model matches GitHub Actions' `id-token: write`: the cloud provider fetches the controller's public keys over HTTPS and checks each token's signature, audience, expiry and subject before it hands out credentials.

## How it works

1. The controller publishes `https://<external-url>/.well-known/openid-configuration` and the key set it points at, `https://<external-url>/.well-known/jwks.json`. Both are public and cacheable for one hour.
2. Pipeline code calls `sparkwing.OIDCToken(ctx, audience)`. The runner executing the node sends `POST /api/v1/runs/{id}/oidc-token` with its own credential.
3. The controller signs a token only for a caller holding a live claim on that run, reads every claim from the run's stored rows, and never from the request, except the audience.
4. The pipeline hands the token to the cloud provider's token exchange.

## Turn it on

The issuer is the controller's `--external-url`. It must be an `https` origin with no path, query or fragment, because every cloud provider fetches `<issuer>/.well-known/openid-configuration` and compares the token's `iss` claim to the issuer byte for byte. AWS also expects no port.

Generate an RSA key and give it to the controller:

```sh
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out oidc-signing.pem
```

| Flag | Environment | Meaning |
|---|---|---|
| `--oidc-key-file` | `SPARKWING_OIDC_KEY` (the PEM itself) | RSA private key, PKCS #1 or PKCS #8 PEM, at least 2048 bits, that signs every token. Without it the controller issues no tokens and the three routes answer 404. |
| `--oidc-previous-key-file` | `SPARKWING_OIDC_PREVIOUS_KEY` (the PEM itself) | The key that signed before the current one, as a private or public key PEM. The controller publishes it in the key set and never signs with it. |
| `--oidc-token-ttl` | | Token lifetime. Default `10m`, at most `1h`, at least `1m`. |

The controller refuses to start when a key is set and `--external-url` is empty or is not an `https` origin. It never logs key material; the startup line names the key ids.

Each key's `kid` is its RFC 7638 JWK thumbprint, so the same key always publishes the same id.

### Rotate the key

1. Start the controller with the new key in `--oidc-key-file` and the old key in `--oidc-previous-key-file`. New tokens carry the new `kid`; both keys verify.
2. Wait at least one token lifetime plus the key set's one-hour cache time, so every relying party has fetched the new key and every old token has expired.
3. Drop `--oidc-previous-key-file`.

A token signed by a key absent from the key set fails verification everywhere, so skipping step 2 breaks in-flight exchanges.

## Signing algorithm

Tokens are signed with RS256. It is the one algorithm every target accepts: AWS STS accepts RS256, RS384, RS512, ES256, ES384 and ES512; Google Cloud workload identity federation accepts RS256 and ES256; Microsoft Entra workload identity federation supports only RS256-signed issuer tokens; Vault's JWT auth method accepts RS256.

## The token

Header: `{"alg": "RS256", "kid": "<thumbprint>", "typ": "JWT"}`.

| Claim | Value |
|---|---|
| `iss` | The controller's external URL, for example `https://api.sparkwing.dev`. |
| `aud` | The audience the caller asked for, as one string. |
| `sub` | The subject described below. |
| `iat`, `nbf` | Issue time. |
| `exp` | Issue time plus the token lifetime, shortened to the requesting credential's own expiry when that comes first. |
| `jti` | 128 random bits, hex. |
| `team` | The run's team slug. |
| `pipeline` | The pipeline name. |
| `trigger` | `webhook`, `cron` or `manual`, described below. |
| `runner_kind` | `runner`, `github-actions`, `user` or `service`, described below. |
| `ref` | `refs/heads/<branch>` when the run names a branch; absent otherwise. |
| `sha` | The commit the run names; absent when it names none. |
| `repository` | `<host>/<owner>/<name>`, for example `github.com/acme/api`, when the run names a repository; absent otherwise. |
| `run_id` | The run id. |

### Subject format

```
team:<team>:pipeline:<pipeline>:trigger:<trigger>:runner:<runner_kind>:ref:<ref>
```

For example, a push to `main` delivered by the GitHub webhook and executed by a team runner:

```
team:acme:pipeline:deploy:trigger:webhook:runner:runner:ref:refs/heads/main
```

The format is stable. Segments run from the coarsest to the finest, so a trust condition that ends in `*` narrows by prefix. `<ref>` is empty when the run names no branch, which leaves the subject ending in `:ref:`. No segment value contains `:`, `*`, `?` or whitespace: the controller refuses to sign for a run whose pipeline or branch contains one (422), so a submitted value cannot forge a later segment or match as a wildcard.

### What each value proves

The controller vouches for the team: it comes from the claimed run's row. The other values are as trustworthy as the path that created the run.

- `trigger` is `webhook` when the run came from a GitHub push delivery whose signature the controller verified; the `ref`, `sha` and `repository` then come from that delivery. It is `cron` when the controller's own schedule started the run; `ref` and `repository` then come from the schedule, which only a `runs.control` principal can write. Every other start, including the CLI, the dashboard, the API and a retry, is `manual`, and its `pipeline`, `ref`, `sha` and `repository` are whatever the submitter sent.
- `runner_kind` names the credential that holds the claim: `runner` for a runner token, `github-actions` for the credential a GitHub Actions job received through the [runner exchange](github-actions-runners.md), `user` for a person's token or session, which is how a laptop run reaches the controller, and `service` for a service token.

A deploy role should therefore require `trigger:webhook` or `trigger:cron`, and `runner:runner`, in the subject. A trust policy that accepts `trigger:manual` or `runner:user` accepts any code a team member with `runs.write` chooses to run.

A GitHub Actions job holding a claim can request a token too, with `runner:github-actions`. Such a job's credential is bound to its own repository's push, so its `ref` and `sha` are the push's. GitHub's own ID token proves the same repository facts with GitHub as the issuer; use Sparkwing's when the trust policy should name the Sparkwing team and pipeline.

## The token route

`POST /api/v1/runs/{id}/oidc-token` with scope `nodes.claim` or `triggers.claim`.

```json
{"audience": "sts.amazonaws.com"}
```

```json
{"token": "eyJ...", "expires_at": "2026-01-01T00:10:00Z"}
```

- `audience` is required: 1 to 256 printable ASCII characters with no spaces, which admits URLs and plain identifiers.
- The caller must hold a live claim on a node of the run or on the trigger that created it, the same rule that fences a runner's secret reads. The run's team comes from that claim. A run the caller holds no claim on answers 404, whether it exists in the caller's team, in another team, or nowhere, so the route reveals nothing about other runs.
- Each claim holder may mint 30 tokens per run, refilled over five minutes; past that the route answers 429 with `Retry-After`.
- A controller with no signing key answers 404.

## Get a token in a pipeline

```go
token, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com")
```

`OIDCToken` works in any node a runner executes against a controller. A run with no controller behind it, such as a local run, gets `sparkwing.ErrOIDCUnavailable`.

The AWS SDKs and CLI read a web identity token from a file named by `AWS_WEB_IDENTITY_TOKEN_FILE`:

```go
func awsEnv(ctx context.Context, roleARN string) ([]string, error) {
	token, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com")
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "aws-web-identity-*")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(token); err != nil {
		return nil, err
	}
	return []string{
		"AWS_ROLE_ARN=" + roleARN,
		"AWS_WEB_IDENTITY_TOKEN_FILE=" + f.Name(),
		"AWS_ROLE_SESSION_NAME=sparkwing",
	}, nil
}
```

Pass those variables to the step that runs `aws` or the SDK. The SDK re-reads the file when its credentials expire, so a step longer than the token lifetime writes a fresh token to the same file first. To exchange by hand:

```sh
aws sts assume-role-with-web-identity \
  --role-arn arn:aws:iam::123456789012:role/sparkwing-deploy \
  --role-session-name sparkwing \
  --web-identity-token "$TOKEN"
```

## AWS

Create the OIDC provider once per account. AWS verifies the issuer's TLS certificate against its trusted CAs, so the thumbprint only matters for a private CA:

```sh
aws iam create-open-id-connect-provider \
  --url https://api.sparkwing.dev \
  --client-id-list sts.amazonaws.com
```

Trust policy for the role:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::123456789012:oidc-provider/api.sparkwing.dev"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"api.sparkwing.dev:aud": "sts.amazonaws.com"},
      "StringLike": {"api.sparkwing.dev:sub": "team:acme:pipeline:deploy:trigger:webhook:runner:runner:ref:refs/heads/main"}
    }
  }]
}
```

AWS evaluates only `aud` and `sub` from a generic OIDC provider and ignores the custom claims, which is why the subject carries every value a trust policy needs. Use `*` for a segment you do not restrict, for example `team:acme:pipeline:deploy:trigger:*:runner:runner:ref:refs/heads/main`.

## Google Cloud

```sh
gcloud iam workload-identity-pools create sparkwing --location=global
gcloud iam workload-identity-pools providers create-oidc api-sparkwing-dev \
  --location=global --workload-identity-pool=sparkwing \
  --issuer-uri=https://api.sparkwing.dev \
  --allowed-audiences=sparkwing-gcp \
  --attribute-mapping='google.subject=assertion.run_id,attribute.team=assertion.team,attribute.pipeline=assertion.pipeline,attribute.trigger=assertion.trigger,attribute.runner_kind=assertion.runner_kind,attribute.ref=assertion.ref' \
  --attribute-condition="assertion.team == 'acme' && assertion.runner_kind == 'runner' && assertion.trigger in ['webhook', 'cron']"
gcloud iam service-accounts add-iam-policy-binding deploy@my-project.iam.gserviceaccount.com \
  --role=roles/iam.workloadIdentityUser \
  --member='principalSet://iam.googleapis.com/projects/123456/locations/global/workloadIdentityPools/sparkwing/attribute.pipeline/deploy'
```

`google.subject` is limited to 127 bytes, which a long subject can pass, so the mapping above uses the run id and conditions on the individual claims instead. Request tokens with `sparkwing.OIDCToken(ctx, "sparkwing-gcp")`, and write a credential configuration with `gcloud iam workload-identity-pools create-cred-config ... --credential-source-file=<token file>`.

## Azure

Entra matches the subject exactly, so each federated credential names one subject:

```sh
az ad app federated-credential create --id <app-object-id> --parameters '{
  "name": "sparkwing-deploy-main",
  "issuer": "https://api.sparkwing.dev",
  "subject": "team:acme:pipeline:deploy:trigger:webhook:runner:runner:ref:refs/heads/main",
  "audiences": ["api://AzureADTokenExchange"]
}'
```

Request tokens with `sparkwing.OIDCToken(ctx, "api://AzureADTokenExchange")` and sign in with `az login --service-principal -u <client-id> -t <tenant-id> --federated-token "$TOKEN"`.

## Vault

```sh
vault auth enable jwt
vault write auth/jwt/config oidc_discovery_url=https://api.sparkwing.dev bound_issuer=https://api.sparkwing.dev
vault write auth/jwt/role/deploy role_type=jwt user_claim=sub \
  bound_audiences=vault \
  bound_claims_type=glob \
  bound_claims='{"team":"acme","pipeline":"deploy","trigger":"webhook","runner_kind":"runner","ref":"refs/heads/main"}' \
  token_policies=deploy token_ttl=10m
```

Request tokens with `sparkwing.OIDCToken(ctx, "vault")` and log in with `vault write auth/jwt/login role=deploy jwt="$TOKEN"`.
