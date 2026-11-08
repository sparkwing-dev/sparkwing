# Deploy pipeline credentials: proposed IAM role, cluster access and parameters

Status: applied 2026-11-04 on Korey's go-ahead (decision 96). The role, policy, access entry, RBAC and AppProject are on kikd-infra main, and the two Sparkwing Argo applications read `sparkwing-dev/sparkwing-gitops`. The `deploy` pipeline in `.sparkwing/jobs/deploy.go` expects exactly these names; change both together. What remains is under [What a person does](#what-a-person-does).

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
- `trigger:push` and `ref:refs/heads/main` admit only runs the GitHub App started for a push to main. A personal machine, a CLI upload, a pull request and a branch run all mint other subjects. A retry keeps its source run's identity, so a member with `runs.write` who retries an old push run still gets this role; the pipeline, not the policy, stops that (see [Retries and old runs](#retries-and-old-runs)).
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

The policy above cannot delete images, change tags in place (the repositories keep immutable
tags), read other parameters, or touch the database, Route 53 or IAM.

The deploy role gets no RDS permission. Recovery uses RDS point-in-time recovery.

## Cluster access

An EKS access entry maps the role to the Kubernetes group `sparkwing-deployer`, type `STANDARD`, with no AWS-managed access policy. Argo applies every change from GitOps, so the role only reads; it cannot patch an Argo application, so the pipeline does not ask for a hard refresh and Argo finds each commit by polling. The RBAC lives in kikd-infra (`k8s/apps/sparkwing-deployer-rbac.yaml`), outside anything the deploy key can write:

| Scope | Resources | Verbs | Used for |
| --- | --- | --- | --- |
| `argocd` | `applications.argoproj.io` named `sparkwing`, `sparkwing-staging` | get, patch | Hard refresh after each GitOps push, then Synced/Healthy wait |
| `sparkwing`, `sparkwing-staging` | `deployments`, `replicasets` (apps) | get, list, watch | `kubectl rollout status`, image check |
| `sparkwing`, `sparkwing-staging` | `pods`, `pods/log` | get, list | WARN and ERROR scan of controller and launcher logs |
| cluster | `nodes` | get, list | which node carries a smoke run's `sparkwing.dev/used-run` |
| cluster | `nodeclaims.karpenter.sh` | get, list | one NodeClaim per smoke run |

## SSM parameters (SecureString, default `aws/ssm` key)

| Name | Holds | Scope |
| --- | --- | --- |
| `/kikd-prod/sparkwing/deploy/sparkwing-gitops-deploy-key` | ed25519 private key of a write deploy key | `sparkwing-dev/sparkwing-gitops` only |
| `/kikd-prod/sparkwing/deploy/ci-smoke-deploy-key` | ed25519 private key of a read-only deploy key | `sparkwing-dev/sparkwing-ci-smoke` only |
| `/kikd-prod/sparkwing/deploy/staging-gate-token` | staging team token, `runs.read,logs.read,runs.write` | staging fixture team `stg-qa` |
| `/kikd-prod/sparkwing/deploy/smoke-token-qa-team` | production team token, `runs.read,logs.read,runs.write` | `qa-team` |
| `/kikd-prod/sparkwing/deploy/smoke-token-qa-team-b` | production team token, `runs.read,logs.read,runs.write` | `qa-team-b` |

The full `example` run also reads `EXAMPLE_PROBE_TOKEN` from its target fixture team's Sparkwing secrets, rather than SSM. It holds a separate token for that team with `runs.read,logs.read,runs.write`. Its lifecycle probes use it to start, cancel, retry and read `example-probe` runs from inside the Cloud job. Without it they skip.

The production controller also scopes `DEPLOY_SCAN_STAGING_TOKEN` and
`DEPLOY_SCAN_PRODUCTION_TOKEN` to the `deploy` pipeline: a same-revision child
keeps its parent pipeline identity when selecting secrets. They hold fixture-team
tokens for starting and observing the full example on staging and production.
The deploy starts the scan through the authenticated child-start route; it inherits
the deploy's verified-default repository identity instead of a manually asserted
identity. No scan-submit token is used. The detached scan records its result through the runner user's installed Bitwing
and normal ledger. See [deployment instructions](../../../DELIVERY.md#deploying-sparkwing-cloud).

These are long-lived secrets. They never sit on a runner's disk past the job (the job writes each to a 0600 file in its temporary directory and removes the directory) and are masked in logs, but whoever holds the role can read them. Each token should carry an expiry and a rotation date in the ink vault runbook.

## Retries and old runs

A retry of an old push run reuses that run's outputs and mints the same `trigger:push` subject, so the trust policy alone would let it promote older digests. The pipeline refuses this in two places:

- Every deploy job (`staging-deploy`, `production`) runs `git ls-remote origin refs/heads/main` before changing anything and stops unless the candidate commit is main's tip.
- `production` refuses a candidate version below the version production runs. Untagged main commits use Go's canonical pseudo-version `vX.Y.(Z+1)-0.YYYYMMDDhhmmss-SHA12`, computed from the highest reachable release tag, UTC committer time and the first 12 SHA characters before building. No temporary tag is created. Semantic version ordering places the candidate after its base release and before the next release. Image tags, labels, version checks and summaries all use this version. A prerelease base uses Go's corresponding prerelease pseudo-version form. Release-tag builds keep their exact version.

## Pipeline flags and the trust policy

Only a push to main can assume the role, and a push-started run carries no command-line arguments. `--stop-after`, `--promote-proven` and `--version` therefore reach a real deploy only as `args:` on the `deploy` entry in `.sparkwing/sparkwing.yaml`. From a terminal they work only with `--dry-run`. A real run started from a terminal fails at the role assumption (`runner:user`, `trigger:manual`).

## Promotion on a recorded staging pass

`--promote-proven` reads the `staging-gate` output of the newest successful default-branch `deploy` run (`RefToLastRun`). The lookup cannot skip runs: it returns the newest successful run or nothing. So a promotion re-emits the record it promoted on as its own `staging-gate` output, and the record keeps the time its gate ran, so chaining promotions never stretches `--proven-max-age`. A newest run that holds no usable record stops the promotion; deploy staging again.

The lookup reads only `deploy` runs of the reading run's own repository, because a reference to a pipeline's own last run is scoped to the reader's repository. A `deploy` pipeline in another repository of the same team cannot publish a record the production job reads.

## First deploy over an unversioned image

The production downgrade guard requires image tags it can order as release versions or service
build stamps. A hand-built tag such as `commit-9fe8aca9f` stops the pipeline with this remedy,
done once by hand:

1. Confirm the live store matches the candidate: `SELECT version, baseline_digest FROM sparkwing_schema_version` must show the baseline the `build-images` summary prints.
2. Pin `controller.image.tag`, `sparkwing-runner-bundle.launcher.image.tag`, `jobImage.tag` and `jobImage.digest` in `<env dir>/helm-values.yaml` to images this pipeline built (they carry the label).
3. Render without the cluster-scoped documents, which kikd-infra applies: `helm template <release> charts/sparkwing-full --namespace <namespace> -f <env dir>/helm-values.yaml | yq 'select(.metadata.namespace != null)' > <env dir>/rendered.yaml`, then commit and push. Committing the raw render would make Argo refuse the sync, because the `sparkwing` project permits no cluster-scoped kinds.

Later deploys then compare image versions and run unattended.

## What a person does

1. Done 2026-11-04: the role, policy, access entry, RBAC and AppProject are applied, with `sparkwing_deploy_team` defaulting to `spark-sharks`. Confirm the production team slug that covers `sparkwing-dev/sparkwing`; a different slug is a one-line Terraform change.
2. Allow deploy keys in the `sparkwing-dev` organisation (it refuses them today), then create the write key on `sparkwing-gitops` and the read-only key on `sparkwing-ci-smoke` and store their private halves; mint the three team tokens and store them.
3. Install the GitHub App so repository-scoped runs exist in production: the production team's installation must cover `sparkwing-dev/sparkwing` (the push that starts a deploy) and the WSL workstation and Mac must be enrolled as repository-scoped runners for it (decision 82). The staging `stg-qa` team and the production `qa-team` and `qa-team-b` installations must also cover `sparkwing-dev/sparkwing`, because the gate and the smoke trigger `example` and `deploy-smoke` at the candidate commit, and staging's must cover `sparkwing-ci-smoke`.
4. Run `sparkwing run deploy -- --dry-run --stop-after staging` from a checkout and read the printed actions.
5. The `deploy` entry declares `on: push` for `main` with `args: {stop-after: staging}`. Watch the push deploy and gate staging. Remove the `args` line after that pass so the next push promotes to production.

## Decisions for Korey

1. **Persistent runners.** Decided: accepted (see [Runner isolation](#runner-isolation-accepted)).
2. **Run-now of main.** `trigger:push` refuses a console or CLI "run now" of `main` (`trigger:manual` with `ref:refs/heads/main`). Push-only does not keep members with `runs.write` away from the role: they can retry any push run, and a retry keeps `trigger:push`. What keeps a retry from deploying old code is the pipeline's main-tip and version checks above. A second subject for run-now would let a member deploy main's tip without a push, which those checks already allow through a retry. Recommendation: push only; a redeploy is an empty commit.
3. **Long-lived tokens in SSM.** The staging and QA team tokens could instead come from the controllers trusting this OIDC issuer, which is new server-side work. Recommendation: SSM tokens with expiry until that exists.
4. **Bot commit identity.** GitOps commits are authored as `Sparkwing deploy <deploy@sparkwing.dev>` with the host's git config ignored, so a deploy behaves the same on either machine or on Cloud and never needs a signing agent.
5. **Candidate version.** Superseded by Korey's 2026-11-05 main-deploy decision: use Go's canonical pseudo-version `vX.Y.(Z+1)-0.YYYYMMDDhhmmss-SHA12`, for example `v0.69.3-0.20261106041046-718a7591f23a`. Release tags supply the base. HEAD needs no release tag, and the deploy does not invoke the GitHub release workflow. Registry tags are immutable, and a rerun reuses the same image pair after checking its revision labels.

## Runner isolation (accepted)

Korey accepted persistent deploy runners. The workstation's deploy runner runs as Korey's own user, alongside the agents. What that accepts, plainly:

- Any agent or process running as that user can read a deploy job's files and environment while it runs: the STS session (one hour, this role's permissions), the GitOps deploy key, the staging and QA team tokens, and the ECR password.
- Another job on the same runner can leave binaries behind on PATH or in shared directories, which a later deploy job could run with those credentials.

The cheap mitigations the pipeline applies:

- Each job writes secrets only into its own directory, `sparkwing-deploy-<run id>-<job>-*` under the temporary directory, mode 0600 files, removed when the job ends.
- Each job first removes any `sparkwing-deploy-*` directory older than the longest job budget, so a killed job's secrets do not outlive it.
- Tool binaries (`aws`, `kubectl`, `helm`, `git`, `ssh`, `go`, `bash`, and `node` and `pnpm` for the dashboard build) resolve to absolute paths in `/opt/sparkwing-deploy/bin`, and every child process, including kubectl's `aws` credential plugin, gets only that directory plus `/usr/bin:/bin` on PATH. Installing pinned tools there, owned by root and not writable by Korey's user, is what makes this a defence. A real deploy refuses to start without the directory and never falls back to the runner's PATH (decision 86); only a credential-free `--dry-run` resolves from PATH. DELIVERY.md's "Deploy toolchain" section has the install commands.

## GitOps scope (decided: confine the deploy credential to Sparkwing)

A GitHub deploy key reaches its whole repository, so a write key on kikd-gitops can change every application there (bitwing, moonborn, okbot, stormwing and the rest). Korey chose to confine it. Even confined, the GitOps deploy key is effectively production root, because it can deploy a pod that reads the controller's OIDC signing key; that is acceptable while the deploy machines already hold cluster admin.

**Chosen: a separate repository, `sparkwing-dev/sparkwing-gitops`,** holding the `sparkwing` and `sparkwing-staging` directories unchanged. The alternative is a branch of kikd-gitops tracked only by the two Sparkwing applications.

| | Separate repository | Branch in kikd-gitops |
| --- | --- | --- |
| Key scope | The key reaches nothing else, by construction | The key still reaches every branch, including `main`; only a ruleset that restricts updates on every other branch, with the key off the bypass list, holds it to its branch, and a mistake in that ruleset silently widens it |
| Argo confinement | AppProject `sourceRepos` names the one repository | AppProject cannot restrict by branch; another app in the `sparkwing` project could point at `main` |
| Cost | One more repository; history before the move stays in kikd-gitops | No new repository, but two long-lived lines of history in one repository that must never merge |

**Argo.** AppProject `sparkwing` (kikd-infra `k8s/apps/sparkwing-project.yaml`) allows only the new repository as a source and only the six namespaces the chart renders into: `sparkwing`, `sparkwing-cloud-jobs`, `sparkwing-cloud-warm` and their `sparkwing-staging` counterparts. Restricting it to `sparkwing` and `sparkwing-staging` alone would refuse every sync, because the chart also renders objects into the job and warm namespaces. The two Applications move to that project and repository in a separate commit (`eaaf94c`), so it can land only after the new repository is filled.

**Cluster-scoped objects (decided: kikd-infra applies them).** The chart renders cluster-scoped objects: its two namespaces, the runner-boundary ClusterRole and ClusterRoleBinding, and the Cloud job ValidatingAdmissionPolicies and bindings. If the AppProject allowed those kinds, a malicious commit to sparkwing-gitops could create a ClusterRoleBinding to `cluster-admin` or weaken an admission policy. So the AppProject `sparkwing` permits no cluster-scoped kinds, and the deploy pipeline drops them from `rendered.yaml`: it removes each document of a known cluster-scoped kind, logs what it left out, and fails the render on any other document without `metadata.namespace`. kikd-infra keeps them in `k8s/sparkwing-cluster/staging.yaml` and `production.yaml`, applied by the Argo application `sparkwing-cluster` (project `kikd`, `k8s/apps/sparkwing-cluster.yaml`).

**Cloud admission images (decided: a param ConfigMap).** The Cloud job policies once embedded the runner and Docker image digests as CEL literals, and the launcher refuses to start unless the live policy equals the one it would build. With the policies in kikd-infra, every release would have left them on the old digests, so the new launcher could not start and a rollback could not restore them. The policies now declare `paramKind` ConfigMap and their bindings `paramRef` to `sparkwing-cloud-admission` in the job namespace, with `parameterNotFoundAction: Deny`. The chart renders that ConfigMap from `launcher.jobImage` and `launcher.dockerImage`, so it travels in `rendered.yaml` with the launcher Deployment, and the cluster-scoped objects are identical across releases and rollbacks. The launcher checks the policies, the bindings and that the ConfigMap holds exactly its own images. Trust is unchanged: whoever can write `rendered.yaml` already chooses the launcher image, which is the only creator of Jobs there, and the launcher's Role grants it `get` on that one ConfigMap and no write. Deny on a missing ConfigMap fails closed: Allow would skip every rule, not just the image ones. A rollback of `rendered.yaml` to a version from before the ConfigMap prunes it and stops all Cloud Pods and Jobs, so the first release carrying it is the rollback floor.

A release that changes them updates kikd-infra before its deploy, staging first, then production: render the chart at the new tag (`helm template <release> charts/sparkwing-full --namespace <ns> -f <env dir>/helm-values.yaml`), copy its cluster-scoped documents into the environment's file, land that on kikd-infra main and wait for `sparkwing-cluster` to report Synced. Add permissions before the deploy and remove them after the old version is gone. `sparkwing-cluster` prunes, so deleting an object from the file deletes it from the cluster; a Namespace leaves the file only on purpose, because deleting it deletes everything in it.

**Argo refresh.** The deploy role may get and patch only the named `sparkwing` and `sparkwing-staging` Applications. After each GitOps push, the pipeline sets `argocd.argoproj.io/refresh=hard` and then waits for the exact revision to be Synced and Healthy. Patch refusal fails that phase with a permission remedy; the pipeline does not fall back to Argo's periodic poll.

**Pipeline.** The pipeline writes only `sparkwing-dev/sparkwing-gitops`, with the `sparkwing-gitops-deploy-key` parameter, in the same directory names.

**The move (done 2026-11-04).** `sparkwing-dev/sparkwing-gitops` main is kikd-gitops `456e104` filtered to `sparkwing/` and `sparkwing-staging/` with history kept, trees identical. Argo reads it through ExternalSecret `argocd/sparkwing-gitops-repo` (the same token as kikd-gitops), and both applications moved to it with no change to any live object. The two directories were then deleted from kikd-gitops. The organisation has deploy keys disabled, so the write deploy key and its parameter wait for Korey.
