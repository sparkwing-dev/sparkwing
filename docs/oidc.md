# OIDC tokens for cloud roles

A controller with a signing key is an OpenID Connect issuer. A run asks it for a short-lived ID token that names the run's team, repository, pipeline, trigger, ref and the kind of host it ran on, and exchanges that token with AWS, Google Cloud, Azure or Vault for credentials. No cloud key is stored in Sparkwing secrets, and a cloud role trusts only the pipelines its trust policy names.

The model matches GitHub Actions' `id-token: write`: the cloud provider fetches the controller's public keys over HTTPS and checks each token's signature, audience, expiry and subject before it hands out credentials.

## How it works

1. The controller publishes `https://<external-url>/.well-known/openid-configuration` and the key set it points at, `https://<external-url>/.well-known/jwks.json`. Both are public and cacheable for one hour.
2. Pipeline code calls `sparkwing.OIDCToken(ctx, audience)`. The host sends `POST /api/v1/runs/{run}/oidc-token` using the job’s live, run-bound capability with `oidc.mint`.
3. The controller signs a token only for a caller holding a live claim on that run, reads every claim from the run's stored rows, and never from the request, except the audience.
4. The pipeline hands the token to the cloud provider's token exchange.

Purely local runs have no OIDC issuer. To obtain tokens, run the pipeline through
a controller with OIDC configured as described in [Turn it on](#turn-it-on).
If the controller has no signing key, `sparkwing.OIDCToken` reports `not_found`
with a setup explanation and a pointer to this guide.

## Turn it on

The issuer is the controller's `--external-url`. It must be an `https` origin with no path, query or fragment, because every cloud provider fetches `<issuer>/.well-known/openid-configuration` and compares the token's `iss` claim to the issuer byte for byte. AWS also expects no port.

Generate an RSA key and give it to the controller as the `oidc-key` file in its
[`--credentials-dir`](self-hosting.md#controller-credentials):

```sh
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out oidc-signing.pem
```

| Setting | Meaning |
|---|---|
| `oidc-key` credential | RSA private key, PKCS #1 or PKCS #8 PEM, at least 2048 bits, that signs every token. Only its owner may read the file (mode 0400 or 0600), or its group when that group is one of the controller process's own, such as a Kubernetes pod's `fsGroup` (mode 0440); startup refuses any other group or user access. Without it the controller issues no tokens and the three routes answer 404. |
| `oidc-key.published` credential | A second key's public half, as a public key PEM (`openssl pkey -in key.pem -pubout`), that the key set publishes and that never signs: the next key before a rotation, the previous key after one. Startup refuses private key material in this file. |
| `--oidc-token-ttl` | Token lifetime. Default `10m`, at most `1h`, at least `1m`. |

The controller refuses to start when a key is set and `--external-url` is empty or is not an `https` origin. It never logs key material; the startup line names the key ids.

Each key's `kid` is its RFC 7638 JWK thumbprint, so the same key always publishes the same id.

### Rotate the key

Relying parties cache the key set for up to an hour, so a new key is published before anything signs with it:

1. **Publish the next key.** Keep the current key in `oidc-key` and put the new key's public half in `oidc-key.published`. Tokens still carry the current `kid`.
2. **Wait longer than the one-hour cache time**, so every relying party's cached key set holds the new key.
3. **Switch signing.** Put the new key in `oidc-key` and the old key's public half in `oidc-key.published`. New tokens carry the new `kid`, and tokens the old key signed keep verifying.
4. **Wait longer than one token lifetime**, so every old-key token has expired, then remove `oidc-key.published`.

Switching signing before step 2 has passed makes a relying party that cached the old key set reject new tokens until its cache expires.

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
| `exp` | Issue time plus the token lifetime, shortened by the claim’s ancestry expiry, revocation deadline and binding deadline. |
| `jti` | 128 random bits, hex. |
| `team` | The run's team slug. |
| `pipeline` | The pipeline the run speaks for. A child at its parent's exact revision speaks for its parent's pipeline. |
| `trigger` | `push`, `pull_request`, `release`, `create`, `delete`, `cron`, `actions` or `manual`, described below. |
| `runner_kind` | `runner`, `personal`, `github-actions`, `user` or `service`, described below. |
| `runner_environment` | `fresh` or `persistent`, described below. Like GitHub Actions' `runner_environment` (`github-hosted` or `self-hosted`), it says whether a job of another run could have touched the host. |
| `ref` | The ref the controller established: the verified delivery's ref (`refs/pull/<number>/head` for a pull request, `refs/heads/<base>` for a merged pull request's `closed` event, `refs/tags/<tag>` for a tag push or release, otherwise `refs/heads/<branch>`), the default branch an admin-armed schedule follows, the push a GitHub Actions credential is bound to, or the branch or tag head (`refs/heads/<branch>` or `refs/tags/<tag>`) the GitHub App resolved for a `manual` run whose code Sparkwing fetched and ran there. Absent for every other `manual` run. |
| `sha` | The commit the controller established alongside `ref`; absent whenever `ref` is. A branch `delete` event's run has `ref` naming the deleted branch, which has no commit, so `sha` is the head of the repository's default branch when the delivery arrived, and that is the code the run executes. |
| `repository` | GitHub's `<owner>/<name>`, for example `acme/api`, as GitHub reported it when the run was admitted. Absent when the run proved no repository. |
| `repository_id` | GitHub's numeric repository id as a string, like GitHub's own claim; absent when the run proved no repository. |
| `run_id` | The run id. |

### Subject format

```
team:<team>:repository_id:<repository_id>:pipeline:<pipeline>:trigger:<trigger>:runner:<runner_kind>:runner_environment:<runner_environment>:ref:<ref>
```

`<repository_id>` is GitHub's numeric repository id, which survives a rename or a transfer. Read it with `gh api repos/<owner>/<name> --jq .id`. It is set for a run a signed GitHub App delivery started, a run bound to a GitHub Actions push, a fire of an admin-armed default-branch schedule, a run whose code Sparkwing fetched from the repository itself, and a retry or child run that inherits one of those identities whole. It is empty for every other run, such as an upload from the CLI or a run on the submitter's own machine. `trigger:push`, `trigger:pull_request`, `trigger:release`, `trigger:create`, `trigger:delete`, `trigger:cron` and `trigger:actions` always carry a repository id.

For example, a push to `main` of repository `123456789` delivered by the GitHub App and executed on a launcher pod:

```
team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main
```

A tag push keeps `trigger:push` and uses its tag ref, for example `team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/tags/v1.2.3`. A policy for a protected branch should match `refs/heads/<branch>` explicitly.

The same pipeline run for pull request 42, even one opened from a branch named `main`:

```
team:acme:repository_id:123456789:pipeline:deploy:trigger:pull_request:runner:runner:runner_environment:fresh:ref:refs/pull/42/head
```

The format is stable. Segments run from the coarsest to the finest: a team holds repositories and a repository declares pipelines, so a trust condition that ends in `*` narrows by prefix, and `team:acme:repository_id:123456789:*` admits every pipeline of one repository. A pipeline name is not unique across repositories: a team that subscribes `deploy` on two repositories gets two subjects that differ only in `repository_id`. `<repository_id>` is empty for a run that proved no repository, which leaves `repository_id::` in the subject, and `<ref>` is empty when the `ref` claim is absent, which leaves the subject ending in `:ref:`. Every segment value is printable ASCII with no `:`, `*` or `?`: the controller refuses to sign for a run whose pipeline or branch holds one of those, whitespace, or any other character (400), so a submitted value cannot forge a later segment or match as a wildcard.

### What each value proves

Intake seals every run with the identity it speaks for: a trust class, and the ref, commit, event and pipeline that class establishes. `trigger`, `repository_id`, `ref`, `sha` and `pipeline` read only that sealed identity, never the request body. The controller vouches for the team: it comes from the claimed run's row.

- `trigger` names the trust class:
  - `push`, `pull_request`, `release`, `create`, `delete`: a GitHub App delivery whose signature the controller verified. The `repository_id`, `ref` and `sha` come from that delivery. A pull request's `ref` is `refs/pull/<number>/head` whatever the head branch is called, and its `sha` is the head commit. A `closed` event runs only a merged pull request, at its merge commit, with `ref` `refs/heads/<base>`. Pull requests from forks never run.
  - `cron`: a fire of an admin-armed default-branch schedule: a team admin armed it (through GitHub App automation consent, or as a member who holds `team.admin` when it fires), it follows the repository's default branch as GitHub reports it through the App at fire time, it has no locked commit and no override of arguments, cadence, timezone, overlap or catch-up, and the App resolves its commit at fire time. `ref` is `refs/heads/<default>`, `sha` is that branch's head when the schedule fired, and `repository_id` is set.
  - `actions`: a run bound to a GitHub Actions push. `repository_id`, `ref` and `sha` are the push's, which GitHub's own ID token proved.
  - `manual` with `repository_id`, `repository`, `ref` and `sha`: Sparkwing fetched the code from that repository itself, through the team's GitHub App, at a branch or tag head the App resolved, and ran it there. This covers a fire, override or "run now" of a schedule declared through the GitHub App that is not locked to a commit and does not qualify for `cron`, and a console or CLI run of a GitHub branch or tag that the controller fetched. Sparkwing resolved and verified that head, so `ref` and `sha` are the ones the run executed, as in GitHub's `workflow_dispatch` tokens; a member still chose which branch, tag or arguments to run, which `trigger:manual` records. A run-now on `main` therefore matches a policy that pins `ref:refs/heads/main`, and one on another branch does not; a policy that cares how a run started requires `trigger:push`. An unqualified branch name that resolves both as a branch and as a tag at the same commit claims no `ref` or `sha`.
  - `manual` with `repository_id` and `repository` but no `ref` or `sha`: Sparkwing fetched the code from that repository, but the code ran at another commit than the head the App resolved: an App delivery, console or CLI run, or schedule whose `source:` pin is a branch or tag.
  - `manual` with no `repository_id`, `ref` or `sha`: every other run, including an upload or working tree from the CLI, a run on the submitter's own machine, a commit no branch head selects, a pull request ref, a schedule locked to a commit or armed outside the GitHub App, and a child run that does not run its parent's exact revision. The controller proves none of its repository, ref or commit, so the token claims none of them.
- `repository` is emitted only alongside `repository_id`.
- A child run either runs its parent's exact revision (same repository, ref, commit and code commit) and inherits the parent's identity whole, including its `pipeline`, or is asserted under its own pipeline name and mints `manual` with no `repository_id`, `ref` or `sha`. The accepted plan declares each child pipeline and repository. Award issuance seals the authorized pairs in a separate child-start capability, using the team's registered repositories or live App installation coverage. The claim's source and data scope remain on the parent repository. A child in another repository names a branch the controller resolves there, never a commit, and is asserted. A job may start only the child pipelines its plan declares with `StartsChildren` for its own repository or `StartsChildrenIn("gh:123", "deploy")` for an explicit repository. A retry keeps its source run's OIDC identity, so a retried push stays `push` and a retried schedule fire stays `cron`.
- A `source:` code pin that is a full commit ID keeps the run's identity. A branch or tag pin, unless it resolves to the execution commit itself, runs code whoever can push that ref chooses, so the run mints `manual` with no `ref` or `sha`.
- `runner_environment` is `fresh` when the job ran on a host no job of another run touched: a launcher pod, a Sparkwing Cloud pod or a GitHub-hosted Actions runner. A launcher or Cloud pod can land on a warm node that earlier jobs of the same run used, so `fresh` does not separate jobs within one run. It is `persistent` on every other host: a plain machine, a persistent Kubernetes runner, a member's personal machine, a run "here", and a self-hosted Actions runner. It is the same classification that decides whether verified work may write the shared default-branch cache. A role that deploys should require `runner_environment:fresh`, because on a persistent host an earlier job, possibly another repository's, can leave behind code that the next job runs.
- `runner_kind` names the credential that holds the claim: `runner` for a shared runner token, a launcher pod or a Sparkwing Cloud pod, `personal` for a member's personal machine, `github-actions` for the credential a GitHub Actions job received through the [runner exchange](github-actions-runners.md), `user` for a person's token or session, which is how a laptop run reaches the controller, and `service` for a deployment service or operator token that claims work directly.

A deploy role should therefore require one repository, `trigger:push` on a protected branch, `runner:runner` and `runner_environment:fresh`, for example `team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main`. Branch protection on that repository then decides what reaches `main`; the protection of another repository that subscribes the same pipeline name does not matter, because its runs carry a different `repository_id`. A policy that leaves the repository open, such as `team:acme:repository_id:*:pipeline:deploy:...`, accepts a push to `main` of any repository the team subscribes, and so trusts the weakest branch protection among them. Data records retain their repository, ref and provenance. The controller
authorizes every reference edge. Manual publication retains local provenance,
and pipeline binaries stay local. A self-hosted runner's machine is a different matter: whoever runs code on it can change what the next run there finds. `trigger:cron` means "declared by the repository's configuration on that branch", so it is as protected as the branch. A trust policy that accepts `trigger:manual` with an empty `repository_id` or `runner:user` accepts any code a team member with `runs.write` chooses to run. A GitHub Actions-bound run carries `trigger:actions` with its own `repository_id`, so a policy for it names the repository like any other, and subjects never collide across a team's repositories. A run Sparkwing fetched carries `trigger:manual` with its `repository_id` and, when it ran at a resolved head, that `ref`, so a policy that names the repository but accepts `trigger:*` also accepts any branch or tag of it a member chooses to run, and one that also pins `ref:refs/heads/main` accepts a member's run of main.

A GitHub Actions job holding a claim can request a token too, with `runner:github-actions`. Such a job's credential is bound to its own repository's push, so its `repository_id`, `ref` and `sha` are the push's. GitHub's own ID token proves the same repository facts with GitHub as the issuer; use Sparkwing's when the trust policy should name the Sparkwing team and pipeline.

## The token route

`POST /api/v1/runs/{run}/oidc-token` requires a live awarded or delegated claim with `oidc.mint`, bound to that run. A machine root or account credential cannot call it.

```json
{"audience": "sts.amazonaws.com"}
```

```json
{"token": "eyJ...", "expires_at": "2026-01-01T00:10:00Z"}
```

- `audience` is required: 1 to 256 printable ASCII characters with no spaces, which admits URLs and plain identifiers.
- The caller must hold the live run-bound claim. Missing, expired, revoked, cancelled or ended authority answers 401 `unauthenticated`. A live credential without the grant or matching run binding answers 403 `forbidden`. Request headers and query parameters cannot select the team, holder, repository or signing facts.
- Each claim may mint 30 tokens per run on each controller replica, refilled over five minutes; past that the route answers 429 with `Retry-After`.
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
      "StringLike": {"api.sparkwing.dev:sub": "team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main"}
    }
  }]
}
```

AWS evaluates only `aud` and `sub` from a generic OIDC provider and ignores the custom claims, which is why the subject carries every value a trust policy needs. The policy above admits only fresh hosts; replacing `runner_environment:fresh` with `runner_environment:*` also admits persistent runners. Use `*` at the end to admit several refs, for example `team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/release-*`. A `*` in the trigger segment also admits `manual`, `actions` and `cron` runs, and a `*` in the repository segment admits every repository of the team.

## Google Cloud

```sh
gcloud iam workload-identity-pools create sparkwing --location=global
gcloud iam workload-identity-pools providers create-oidc api-sparkwing-dev \
  --location=global --workload-identity-pool=sparkwing \
  --issuer-uri=https://api.sparkwing.dev \
  --allowed-audiences=sparkwing-gcp \
  --attribute-mapping='google.subject=assertion.run_id,attribute.team=assertion.team,attribute.pipeline=assertion.pipeline,attribute.trigger=assertion.trigger,attribute.runner_kind=assertion.runner_kind,attribute.runner_environment=assertion.runner_environment,attribute.ref=assertion.ref,attribute.repository=assertion.repository' \
  --attribute-condition="assertion.team == 'acme' && assertion.repository_id == '123456789' && assertion.runner_kind == 'runner' && assertion.runner_environment == 'fresh' && assertion.trigger == 'push' && assertion.ref == 'refs/heads/main'"
gcloud iam service-accounts add-iam-policy-binding deploy@my-project.iam.gserviceaccount.com \
  --role=roles/iam.workloadIdentityUser \
  --member='principalSet://iam.googleapis.com/projects/123456/locations/global/workloadIdentityPools/sparkwing/attribute.pipeline/deploy'
```

`google.subject` is limited to 127 bytes, which a long subject can pass, so the mapping above uses the run id and conditions on the individual claims instead. Condition on `repository_id` as well as `pipeline`, because a pipeline name is not unique across a team's repositories. A `trigger` of `push` always carries the repository from the signed delivery. Request tokens with `sparkwing.OIDCToken(ctx, "sparkwing-gcp")`, and write a credential configuration with `gcloud iam workload-identity-pools create-cred-config ... --credential-source-file=<token file>`.

## Azure

Entra matches the subject exactly, so each federated credential names one subject:

```sh
az ad app federated-credential create --id <app-object-id> --parameters '{
  "name": "sparkwing-deploy-main",
  "issuer": "https://api.sparkwing.dev",
  "subject": "team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main",
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
  bound_claims='{"team":"acme","repository_id":"123456789","pipeline":"deploy","trigger":"push","runner_kind":"runner","runner_environment":"fresh","ref":"refs/heads/main"}' \
  token_policies=deploy token_ttl=10m
```

Request tokens with `sparkwing.OIDCToken(ctx, "vault")` and log in with `vault write auth/jwt/login role=deploy jwt="$TOKEN"`.
