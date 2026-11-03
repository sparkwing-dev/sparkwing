# Deploy pipeline credentials: proposed IAM role, cluster access and parameters

Status: proposal for Korey (2026-11-02). Nothing here is applied. The `deploy` pipeline in `.sparkwing/jobs/deploy.go` expects exactly these names; change both together.

The pipeline holds no key. Each deploy job asks the controller for an OIDC token (`sparkwing.OIDCToken(ctx, "sts.amazonaws.com")`), exchanges it with `sts:AssumeRoleWithWebIdentity` for a one-hour session, and runs `aws`, `kubectl` and `git` with only that session. The machine's AWS profile, config and git config are never read (decision 82: per-run credentials, both owned machines may deploy).

## Role `arn:aws:iam::633280902600:role/sparkwing-deploy`

Maximum session duration: 3600 seconds. The OIDC provider `arn:aws:iam::633280902600:oidc-provider/api.sparkwing.dev` already exists.

### Trust policy

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::633280902600:oidc-provider/api.sparkwing.dev"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"api.sparkwing.dev:aud": "sts.amazonaws.com"},
      "StringLike": {"api.sparkwing.dev:sub": "team:<team>:repository_id:1228072760:pipeline:deploy:trigger:push:runner:runner:runner_environment:*:ref:refs/heads/main"}
    }
  }]
}
```

- `<team>` is the production team that owns the `sparkwing-dev/sparkwing` subscription. `1228072760` is that repository's GitHub id (`gh api repos/sparkwing-dev/sparkwing --jq .id`), so a pipeline named `deploy` in another repository of the team cannot assume the role.
- `trigger:push` and `ref:refs/heads/main` admit only runs the GitHub App started for a push to main. A personal machine, a CLI upload, a pull request and a branch run all mint other subjects.
- `runner:runner` admits the repository-scoped runners and Cloud pods. It excludes `personal` and `user`, so a laptop run of the pipeline is refused even on Korey's machines.
- `runner_environment:*` is needed because the WSL workstation and the Mac are persistent hosts. [docs/oidc.md](../../../docs/oidc.md) recommends `fresh` for deploy roles, because an earlier job on a persistent host can leave code behind. Decision 82 accepts that for these two machines; see decision 1 below.

### Permissions policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid": "EcrLogin", "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
    {"Sid": "EcrImages", "Effect": "Allow",
     "Action": ["ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
                "ecr:InitiateLayerUpload", "ecr:UploadLayerPart", "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:DescribeImages"],
     "Resource": ["arn:aws:ecr:us-west-2:633280902600:repository/sparkwing-controller",
                  "arn:aws:ecr:us-west-2:633280902600:repository/sparkwing-runner"]},
    {"Sid": "Cluster", "Effect": "Allow", "Action": "eks:DescribeCluster",
     "Resource": "arn:aws:eks:us-west-2:633280902600:cluster/kikd-prod"},
    {"Sid": "DeployParameters", "Effect": "Allow", "Action": "ssm:GetParameter",
     "Resource": "arn:aws:ssm:us-west-2:633280902600:parameter/kikd-prod/sparkwing/deploy/*"},
    {"Sid": "DecryptParameters", "Effect": "Allow", "Action": "kms:Decrypt", "Resource": "*",
     "Condition": {"StringEquals": {"kms:ViaService": "ssm.us-west-2.amazonaws.com"}}}
  ]
}
```

The role cannot delete images, change tags in place (the repositories keep immutable tags), read other parameters, or touch the database, Route 53 or IAM.

## Cluster access

An EKS access entry maps the role to the Kubernetes group `sparkwing-deployer`, type `STANDARD`, with no AWS-managed access policy. Argo applies every change from GitOps, so the role only reads, plus one patch to ask Argo for a hard refresh. The manifests belong in kikd-gitops beside the Argo applications:

| Scope | Resources | Verbs | Used for |
| --- | --- | --- | --- |
| `argocd` | `applications.argoproj.io` named `sparkwing`, `sparkwing-staging` | get, patch | Synced/Healthy wait, refresh annotation |
| `sparkwing`, `sparkwing-staging` | `deployments`, `replicasets` (apps) | get, list, watch | `kubectl rollout status`, image check |
| `sparkwing`, `sparkwing-staging` | `pods`, `pods/log` | get, list | WARN and ERROR scan of controller and launcher logs |
| cluster | `nodes` | get, list | which node carries a smoke run's `sparkwing.dev/used-run` |
| cluster | `nodeclaims.karpenter.sh` | get, list | one NodeClaim per smoke run |

## SSM parameters (SecureString, default `aws/ssm` key)

| Name | Holds | Scope |
| --- | --- | --- |
| `/kikd-prod/sparkwing/deploy/gitops-deploy-key` | ed25519 private key of a write deploy key | `koreyGambill/kikd-gitops` only |
| `/kikd-prod/sparkwing/deploy/ci-smoke-deploy-key` | ed25519 private key of a read-only deploy key | `sparkwing-dev/sparkwing-ci-smoke` only |
| `/kikd-prod/sparkwing/deploy/staging-gate-token` | staging team token, `runs.read,logs.read,runs.write` | staging fixture team `stg-qa` |
| `/kikd-prod/sparkwing/deploy/smoke-token-qa-team` | production team token, `runs.read,logs.read,runs.write` | `qa-team` |
| `/kikd-prod/sparkwing/deploy/smoke-token-qa-team-b` | production team token, `runs.read,logs.read,runs.write` | `qa-team-b` |

These five are long-lived secrets. They never sit on a runner's disk past the job (the job writes each to a 0600 file in its temporary directory and removes the directory) and are masked in logs, but whoever holds the role can read them. Each token should carry an expiry and a rotation date in the ink vault runbook.

## What a person does

1. Create the role, its two policies and the access entry above (kikd-infra Terraform), and the RBAC in kikd-gitops.
2. Create the two deploy keys in GitHub and store their private halves; mint the three team tokens and store them. Fill `<team>` in the trust policy.
3. Install the GitHub App so repository-scoped runs exist in production: the production team's installation must cover `sparkwing-dev/sparkwing` (the push that starts a deploy) and the WSL workstation and Mac must be enrolled as repository-scoped runners for it (decision 82). The staging `stg-qa` team and the production `qa-team` and `qa-team-b` installations must also cover `sparkwing-dev/sparkwing`, because the gate and the smoke trigger `example` and `deploy-smoke` at the candidate commit, and staging's must cover `sparkwing-ci-smoke`.
4. Run `sparkwing run deploy -- --dry-run`, then `--stop-after staging` from a push-started run, before adding `on: push` for `main` to the `deploy` entry in `.sparkwing/sparkwing.yaml`.

## Decisions for Korey

1. **Persistent runners.** The trust policy must accept `runner_environment:persistent` for the owned machines. The alternative is `fresh` only, which would put every deploy on Cloud. Recommendation: accept persistent for now, because decision 82 already trusts both machines; revisit if a third machine enrolls.
2. **Run-now of main.** `trigger:push` refuses a console or CLI "run now" of `main` (`trigger:manual` with `ref:refs/heads/main`). Adding a second subject for it lets a person redeploy main without a new push, but also lets any member with `runs.write` start a production deploy. Recommendation: push only; a redeploy is an empty commit.
3. **Long-lived tokens in SSM.** The staging and QA team tokens could instead come from the controllers trusting this OIDC issuer, which is new server-side work. Recommendation: SSM tokens with expiry until that exists.
4. **Bot commit identity.** GitOps commits are authored as `Sparkwing deploy <deploy@sparkwing.dev>` with the host's git config ignored, so a deploy behaves the same on either machine or on Cloud and never needs a signing agent.
5. **Candidate version.** A commit with no release tag builds as the next patch version plus `-main.g<12-char commit>`, for example `v0.68.4-main.gbd3c2dd363c5`. Registry tags are immutable, so one commit maps to one image pair, and a rerun reuses it.
