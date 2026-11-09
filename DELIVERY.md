# Delivery

Use the smallest evidence that covers the change. A lead agent owns the decision;
this file is a menu and checklist, not a command that every change must run.

## Xwing workflows

The tracked `.xwing-env.yaml` declares dependency setup, local builds, tests,
and foreground execution. Run `xwing app commands` for the available tasks.
`xwing env land` uses the existing repository gate before fast-forwarding and pushing
main, then applies Xwing's default archive retention. Public releases remain
separate. Stateful CLI commands use their normal configuration; use a candidate
launcher when testing isolated tool state.

## Landing a change

Agents and the lead land every change with `sparkwing run land`, so each
landing goes through Sparkwing itself. From a clean work branch:

```bash
sparkwing run land --sw-dry-run                     # preview; nothing is pushed
sparkwing run land --sw-stop-at verify              # gate and review, stop before the push
sparkwing run land --sw-allow-risk destructive,prod  # land on main
```

Lands run on owned hardware. Policy, gate, review and push use that machine.
The land queue invokes it with `--sw-route local`.

The `preconditions` job refuses a dirty checkout, a detached HEAD, `main`
itself, a CHANGELOG.md that repeats a `###` heading within a release, and a
branch whose merge with a freshly fetched `origin/main`
conflicts, and names the command that fixes each; a branch behind main that
merges cleanly goes ahead. The gate's `policy` job runs next, alongside review: gofmt,
the formatters, em dashes, tracker IDs, comments, test sleeps, tracked
binaries, home resolution, the docs mirror, changelog lint and links, the
required changelog entry, the docs sidebar, the OpenAPI and API-snapshot checks and the
generated CLI reference (`bin/gen-cli-docs.sh --check`). Each answers in
seconds with a warm build cache. Once it passes, the rest of
the gate (the same jobs as `sparkwing run gate`) starts while review continues.
Review is the same job as `sparkwing run review`, under `.review.yaml`. A focused
policy/review run selects both with `--sw-only '*[wy]'` (the job names ending in
`w` or `y`). Selecting review
alone includes preconditions but skips policy. The policy
job collects every finding before it fails; the heavy `gate` job likewise lets
every heavy step report. Every change is reviewed, docs and
comment changes included; the review job never skips by size or path. A diff
whose every file matches `.review.yaml`'s `docs_only` paths (`docs/`, any
`*.md`, `CHANGELOG*`) runs the default review on the cheaper `docs` reviewer
(Claude Haiku) instead of the default model; any other file keeps the default.
The record's `reviewer.model` names the model that ran. Independent default and
security profiles run concurrently, and their findings and sessions are combined
in profile order after both finish. A failed profile leaves the prior review
record unchanged. Review logs time diff assembly, context gathering, each model
invocation (including failed session resumes), settlement and record writing.
Model time includes the reviewer's repository reads and any nested reviewers.
Prompts and findings never appear in these timing logs. Standards prompts keep
the complete rule bodies and omit card routing metadata and terminal vault
backlink lists.
The gate selects its checks from the change scope (decision 96): vet, build, lint,
the critical tests and the TypeScript SDK suite run only when the change
reaches Go source, a non-Markdown file inside a Go package, module files,
`.golangci.yml`, the store schema or, for the SDK suite, `sdk/typescript/`.
A docs, UI or process change runs its own tests (affected packages, frontend
suites when `web/` changed, the docs mirror and source-policy sweeps) plus the
review. `sparkwing run gate -- --full` keeps the broad verification. Pass
`-- --brief '...'` or `-- --skip 'F2=reason'` as for the review pipeline.

The `land` job reads the review JSON from the shared Git directory instead of
resolving a review output blob. Its `verify` step requires the review record's tree to be HEAD's
tree with result `pass` and HEAD unchanged since the preconditions. Its `push`
step fetches `origin/main` and runs
`git push --no-verify origin <HEAD>:refs/heads/main` without force when the
pushed commit's tree is the tree the gate and review judged (the gate runs
every check the pre-push hook makes, the qatrust vet included); a merged tree
pushes through the hook. It carries the
`destructive` and `prod` risks, as release's `push-tag` does. After a refused
push, the step fetches main and checks its ancestry. It merges main only when
main is not an ancestor of the pushed commit. Otherwise it preserves the
remote's reason and says the push can be retried. The job's output is the
landing record (branch, the commit and tree pushed, the gated tree, base,
merges, gate run, review record path and result). The gate's `test` step reports the affected package count and critical tests.

When `origin/main` is not an ancestor of HEAD at push time, the `push` step
merges it into the branch in the checkout (`git merge --no-ff`, the default
git identity and signing; never a rebase). Independent changelog and migration
fragments merge as separate files. Orientation table additions use union merge;
changes to the same rule need review. The push step
then runs `go build` in every module
and the affected-test selection over the files that differ between the gated
tree and the merged tree, then pushes the merge. The review is not run again:
it judged the branch's own diff, and the re-check covers what the merge
brought in. It
merges at most three times per land. A conflict aborts the merge, leaves the
branch as the gate judged it and names `git merge origin/main` to run by hand.
A re-check failure leaves the merge on the branch. The `record-tree` pass
names only the gated tree; the merged tree records no gate pass, and the
landing record names both.

## Checks

### Cutting a release

A release is a tag push. The `release` pipeline resolves the version, renames
the collected `## [Unreleased]` section to the version, stages the release notes,
checks the section covers any schema or wire change since the previous tag,
aligns scaffold and pipeline SDK pins to the chosen version with local replacements,
then commits and atomically pushes that commit to `main` with an annotated
`vX.Y.Z` tag. Run it from the sparkwing checkout under a temporary home,
which the pipeline requires so a
newer embedded schema cannot migrate the operational runs store. The release
factory validates the home before intake opens the store, including for a dry
run. An unset `SPARKWING_HOME` or `$HOME/.sparkwing` is refused with
`isolated_home_required` and a temporary-home remedy. The host supplies that
fixed guidance even before secret discovery completes:

```bash
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-allow-risk destructive,prod
```

The version defaults to a minor bump of the newest tag on origin; pass
`-- --bump patch` for a patch release or `-- --version vX.Y.Z` to name it
(pipeline flags follow `--`). The
pipeline refuses `v1.0.0` and above. The dry run resolves the version, runs the
read-only cut checks (version freshness, Go directives, SDK pins, changelog
links, docs sidebar) and previews the changelog and guide roll; build, lint and the short
tests run only in the real cut.

Manual releases reuse a successful exact-source CI receipt when available. Missing,
pending, failed or unreadable receipts run the local build, lint and short suite
in the detached source worktree. Weekly releases continue to wait for green CI.

The schema changelog gate compares the neutral `internal/store/schema.sql`
baseline with the previous tag. SQL changes at an unchanged schema version
require a version bump and a matching `(Breaking)` changelog entry.

Release lint prints that it is waiting for the box-wide golangci-lint lock,
then uses `--allow-serial-runners` to wait and lint in sequence. The release
job and lint deadline allow 40 minutes total: the ten-minute check allowance
plus 30 minutes for contention. The tier measures that combined wall time,
including waiting. Other lint callers keep their five-minute deadline.

golangci-lint keeps one cache per worktree under that worktree's git
directory (`<git-dir>/sparkwing-cache/golangci-lint`), outside the tree the
pipeline binary's source key walks and removed with the worktree. A cache
shared between trees replays findings under the other tree's paths, so it is
never shared.

The full gate's `lint` step allows ten minutes (decision 96; both modules measure
four to five minutes under load). It runs golangci-lint with
`--allow-serial-runners`, so a neighbor's lint queues it on golangci-lint's own
lock, `$TMPDIR/golangci-lint.lock`, and that wait counts against the ten
minutes (Korey, 2026-11-04, replacing the gate's own lock manager).
Affected landing lint keeps all configured linters, uses bounded workers and
its private worktree cache, and passes `--allow-parallel-runners` to avoid
queueing behind a full scan.

When the lint step fails with "no result after" its deadline, look for the
neighboring run with `ps -eo pid,etime,args | grep '[g]olangci-lint'`. Rerun
the gate once it finishes, or stop it if it is stuck. Nothing was learned about
the tree. An exit 4 saying "Timeout exceeded" is golangci-lint's own
`run.timeout` instead: raise it in `.golangci.yml`.

Version freshness excludes `testdata` fixture modules, matching the committed-module checks.

`push-tag` declares the `destructive` and `prod` risk labels, so only the
second command tags and pushes. The tag push starts the hosted release below.

### After a stable tag

Once the tag is available through the Go module proxy, update main's release
pins from an attached branch whose HEAD is past that tag:

```bash
SPARKWING_HOME="$(mktemp -d)" sparkwing run pre-release --sw-start-at sparkwing-pin --sw-stop-at sparkwing-pin
```

The step bumps `pkg/scaffold/version.go`, `.apidiff/pkg_scaffold.txt`,
`.sparkwing/go.mod`, and `testdata/k8s-e2e/repo/.sparkwing/go.mod` together,
tidies both modules, adds a changelog entry, syncs the docs mirror, and commits
the result. Review that commit and deliver it to main before the next cut.
Run the follow-up even if hosted artifact publication failed, because the
module proxy can already serve the immutable tag. A skipped follow-up surfaces
only at the next cut, whose `version-freshness` step refuses with "scaffold
fallback pin ... is behind latest release"; the fix then is this same command
and a land before the cut, which is how v0.69.0 found v0.68.7's missing pin.

The scaffold fallback refuses pins ahead of the local release tags unless
the exact tag exists on origin. This also verifies hosted release fixtures
whose selected release tag was removed locally.

The local release pipeline ends at the tag push because the new module must
exist before the pins can resolve it. Pre-release leaves the tag's own commit
unchanged because a release cannot depend on itself before publication.
Hosted checks use detached checkouts and report stale pins rather than
committing them. They cannot perform this follow-up for main.

### Release build and publication

Sparkwing owns artifact publication through its native `release` pipeline and
`.github/workflows/release.yaml`. Platform deployment belongs to the platform
repository and runs separately from publishing a Sparkwing tag.

The hosted release builds the dashboard once and shares it with six target
jobs. Each target compiles its supported commands in one Go cache; all 16
binary outputs remain available. Linux image packaging copies those same
executables through the Dockerfiles' `release` stage. The default Dockerfile
target still builds from source.

One final job assembles image manifests, signs the assets, and publishes the
complete release. Publication is serialized across tags; builds remain
parallel. Only a stable version above every published stable release may
become GitHub Latest or move the image `latest` tags. Failed release-list
lookups stop publication. Manual rebuilds use the selected tag's source and
the current workflow's publication tools and image recipes.

#### Signed release index

Every public stable release must finish the `release-index` job in
`.github/workflows/release.yaml`. It reads all GitHub release pages after asset
publication, preserves the verified existing policy, and signs the exact client
format with `SPARKWING_UPDATE_SIGNING_KEY`. Only published stable `v0.x` releases
are added. Twelve-month support windows use GitHub's `published_at`, not the
recovery date. The job shares `release-publication` with asset publication, so a
late older release cannot replace a newer index with an old snapshot.

The publisher writes only `versions.json` and `versions.json.sig` in
`kikd-prod-sparkwing-website`, then invalidates both paths on CloudFront
`E3V178ZREDVPYN`. It reads back the exact bytes from S3 and the public hostname.
Objects use a 60-second cache lifetime. Clients can encounter a mismatched pair
during the two uploads and fail verification until the pair is complete.
Existing clients retain their last verified index.

AWS authentication uses GitHub OIDC and repository variable
`SPARKWING_RELEASE_INDEX_ROLE_ARN`. Its role needs GetObject/PutObject on those
two keys, ListBucket restricted to prefix `versions.json` to establish signature
absence, and CreateInvalidation/GetInvalidation on that distribution. An empty
role variable fails the job. The website deploy must exclude both root objects
before it is enabled. Website documentation manifests and QA indices cannot
supply production signed policy.

After these changes reach the default branch, repair publication without
rebuilding assets or creating a release:

```bash
gh workflow run release.yaml --repo sparkwing-dev/sparkwing --ref main \
  -f tag=v0.68.2 -F index_only=true -F verify_only=false -F publish_images=false
```

For the first migration from the legacy unsigned website manifest only, also pass
`-F bootstrap_index=true`. This requires `index_only=true`, a missing signature,
and the legacy manifest shape. It never bypasses verification of an existing
signature or a signed-policy-shaped document. Later runs require the signed
pair. Tags without public releases are refused. Failed builds, verification-only
runs and prereleases never publish this stable index.

A run retains the generated pair as `signed-release-index` for seven days,
including when an upload fails. An interrupted upload may leave mismatched
objects and block another dispatch. Restore that artifact's matched pair using
the procedure in [the recovery runbook](design/release-index-recovery.md), then
rerun the index-only dispatch. Never bootstrap over a damaged signed policy.

Focused evidence for index changes is `go test ./cmd/verify-release`,
`go test ./bin -run 'TestReleaseIndex|TestTypeScriptReleaseRequiresBothNativeBridges'`,
the workflow publication/queue/pin contracts in `.sparkwing/jobs`, actionlint,
shellcheck on the publisher, and `bin/sync-docs.sh --check`. Generator tests use
the real client verifier. Publisher tests replace AWS and HTTP with local tools
and prove failed preflight performs no upload or invalidation. These checks do
not establish production IAM access or hosted publication success.

The five-minute target includes runner queueing and publication. It has not
yet been measured for this workflow; local fixtures establish its contracts,
not hosted latency.

TypeScript SDK publication also requires native Windows x64 and arm64 jobs.
Each uses matching Node, Go and MSVC tools, builds its protocol bridge, runs
`TestNativeProtocol` and `TestNativeConformance` without skips, and uploads the
bridge with its verification receipt. Both jobs check out the resolved release
source. SDK text uses LF endings so source digests agree across operating systems.
Assembly checks both receipts against that source and the engine release version,
then packs the SDK. The publication job depends on assembly and publishes that
tarball without rebuilding it. Stable releases eligible for GitHub Latest use the
npm `latest` tag only after an independent npm version check. Prereleases use
`next`, and older stable rebuilds use a version-specific `release-` tag.

`bin/publish-typescript.mjs` refuses major versions above 0, a packed `repository.url`
other than this repository (npm provenance rejects it), a packed package name
other than `@sparkwing/sdk`, or a packed version different from the release tag
without `v`. There is no major-version override. An explicit `latest` requires a
stable version newer than every published stable `0.x` from
`npm view @sparkwing/sdk versions --json`; only an E404 means no versions.
Other lookup failures stop publication. Prereleases must use `next`. These checks
prevent a new publication from moving `latest` backwards.

npm publication runs before the immutable GitHub release becomes public. A retry
accepts an existing SDK version only when its registry integrity matches the packed
tarball. Different bytes, missing integrity, failed lookup and failed publication
stop the job. Package lifecycle scripts run during assembly, with publishing scripts
disabled when uploading the checked tarball. An identical retry skips publishing
and leaves all npm tags unchanged. Before each real upload, the publisher runs
`npm publish --dry-run` with the same arguments and logs the JSON file list and
packed size. A failed dry run stops the upload.

The release job publishes `@sparkwing/sdk` through
[npm Trusted Publishing](https://docs.npmjs.com/trusted-publishers) with OIDC and
`id-token: write` on a GitHub-hosted runner. It installs pinned npm 11.6.2 and
prints its version. Trusted publishing requires npm 11.5.1 or newer and Node
22.14.0 or newer. npm generates provenance automatically for the public package
and repository.

In the package's npm settings, configure a GitHub Actions trusted publisher with
organization `sparkwing-dev`, repository `sparkwing`, and workflow filename
`release.yaml`. Leave the environment name unset and allow direct `npm publish`.
Publication needs no Actions secret. Do not set a Node auth token, even to an
empty value.

Native workflow definitions and local graph checks do not establish Windows
runtime support. Both Windows jobs must execute successfully before that
evidence is claimed.

After publication-tool fixes reach `main`, rebuild an existing tag with a new
dispatch, rather than rerunning an old workflow attempt:

```bash
gh workflow run release.yaml --repo sparkwing-dev/sparkwing --ref main \
  -f tag=v0.68.1 -F verify_only=false -F publish_images=false
```

This builds `v0.68.1` source, uses `main`'s publication tools (including the npm
guards), and publishes release assets with image publication skipped. It does
not create or move the Git tag. The trusted publisher must already be configured.

For an accidental npm publication, first move `latest` to a verified good `0.x`
version and check the result:

```bash
npm dist-tag add "@sparkwing/sdk@<version>" latest
npm view @sparkwing/sdk dist-tags --json
```

Within 72 hours, remove the bad version if the
[npm unpublish policy](https://docs.npmjs.com/policies/unpublish/) permits it
(including the dependent-package restriction):

```bash
npm unpublish "@sparkwing/sdk@<bad-version>"
```

After that window, or if npm refuses removal, warn installers instead:

```bash
npm deprecate "@sparkwing/sdk@<bad-version>" "Do not use; install @sparkwing/sdk@<version>."
```

Recheck dist-tags after recovery. The package/version pair can never be reused,
even after unpublishing; publish corrected bytes under a fresh `0.x` version.

For Windows verification without publication, dispatch `release.yaml` on the
branch to check with `verify_only=true`, `publish_images=false`, and a release
version in `tag` (for example, `v0.68.0`). This mode checks out the dispatched
commit, runs both Windows bridge jobs, and uploads their receipts. Dashboard,
release binary builds, package assembly, and publication are skipped. The tag
input supplies the SDK version only and no tag is created or moved. Captured
Go test output is printed when bridge verification fails.

### QA pre-releases

Cloud QA installs unreleased engines through the real signed-release path from
`https://qa-releases.sparkwing.dev`, a private S3 bucket behind CloudFront in
kikd-infra. Only binaries built with the `qatrust` Go tag trust it: the tag adds
the QA public key to `internal/releaseauth` and replaces the version index and
release download origin in `internal/engine`. A QA pre-release version such as
`v0.68.0-qa.1` enforces the QA index instead of passing as a development build.
`sparkwing update` refuses to run in a qatrust build; install the next QA
pre-release instead. `bin/build-release-target.sh` refuses a binary whose build
tags disagree with its version: `qatrust` exactly when the tag ends in `-qa.N`.
pre-push vets touched packages in both flavours.

`bin/qa-release.sh <repo> <commit> <vX.Y.Z-qa.N>` clones the commit, builds the
dashboard and the full release matrix with the tag, signs the assets with the QA
seed from SSM `/kikd-prod/sparkwing/qa-release-signing-key`, appends the version to
the signed QA `versions.json`, and uploads both. It also publishes the Go SDK at
that version in GOPROXY layout under `/goproxy`. The index and the module `list`
are written only if unchanged since they were read, so concurrent runs fail
rather than drop a version. A published QA version is immutable; use the next `-qa.N`.

A qatrust engine compiling a pipeline pinned to a `-qa.N` SDK puts the QA proxy
ahead of the effective GOPROXY (as `go env` reports it, so `go env -w` and
`GOPROXY=off` hold) and adds the sparkwing module to the checksum-database
exemption, seeded from GOPRIVATE when GONOSUMDB is unset. Other pins are untouched.
A qatrust CLI scaffolds Go pipelines with its QA SDK pin and applies those
settings during `go mod tidy`, so `pipeline new` resolves the generated pin
without extra shell configuration. Production CLIs use public SDK pins.
A source build without a release stamp uses the published scaffold fallback.
A QA pipeline's `go.sum` can also be generated by hand
(`GOPROXY='https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct'
GONOSUMDB=github.com/sparkwing-dev/sparkwing go mod tidy`). The TypeScript SDK has
no QA registry package. For Linux/macOS dogfooding, `npm run pack:local --
--version <engine-version> --destination <directory>` in `sdk/typescript` builds
a private local tarball without Windows bridges. npm refuses publication of it.
Published packages still require both verified native Windows bridges.

Accepted QA-only risks:

- A qatrust build also trusts the production key. It reads only the QA origin, but
  a production index cached in the same engine cache makes it fail closed; use a
  separate HOME for QA.
- The QA proxy sees every module path a QA-pinned pipeline fetches, including
  private ones, because `|` must fall through the 403s the bucket returns for
  modules it does not hold.
- The QA Go module zip is unsigned; `go.sum` on first fetch is its integrity.
- The signing seed and `AWS_PROFILE` are in the environment of tools from the
  commit being released, including `pnpm install` in `build-web.sh`; publish only
  trusted commits.
- Clients that read a new `versions.json` before its signature lands fall back to
  their cached index until it does.

QA images: `bin/qa-release.sh` also publishes the controller and runner images, through the
`publish-images` pipeline run on the CLI it just built. The pipeline appends each signed linux
binary, checked against SHA256SUMS, as one layer onto the matching platform of a multi-arch
runtime base, tags each architecture `vX.Y.Z-qa.N-amd64` and `vX.Y.Z-qa.N-arm64`, joins them
under one index tagged `vX.Y.Z-qa.N` (the tag to deploy), and reads every platform back to
check its binary. It is registry operations only, with no Docker daemon or emulation. Name the
bases by digest in `SPARKWING_CONTROLLER_BASE` and `SPARKWING_RUNNER_BASE`; the registry is
`QA_IMAGE_REGISTRY`, by default the AWS profile account's ECR in us-west-2. A base is each
Dockerfile's `runtime` stage, rebuilt only when that stage or its pinned images change, with
`sparkwing run build-images -- --runtime-base --registry <ecr> --push --tag <tag>` on a builder
that runs linux/arm64 natively or under emulation. The hosted release still builds its ghcr.io
images with buildx under QEMU and joins both architectures under one index per tag.

### Deploying Sparkwing Cloud

The default runtime bases in `.sparkwing/jobs/deploy_env.go` must keep both
`cloud-*` and `commit-*` tags on their multi-architecture indexes. The ECR
retention rule matches both patterns and keeps those images outside the rolling
100-image release window. Digest pins alone do not protect an index from
expiration. Keep the platform manifests referenced by each retained index.
When replacing a base, publish its retention tags before updating the pin and
verify that both Linux architectures resolve through that digest.

Production deploys follow main. Releases are separate and occasional: the release tag, 38 CLI
assets, npm packages, signed version index and site publication stay in the release workflow.
The `deploy` pipeline builds its own service images and needs no tag at HEAD or GitHub release run.
It fetches release tags for the base, then computes Go's canonical pseudo-version. After a
stable release, this is `vX.Y.(Z+1)-0.YYYYMMDDhhmmss-SHA12` from the highest reachable tag, the commit's UTC
committer time and its first 12 SHA characters. For example, a commit after `v0.69.2` builds as
`v0.69.3-0.20261106041046-718a7591f23a`. This version matches Go's module stamp without creating
a temporary tag. The same version names image tags, labels, version checks and summaries.
Production compares it with semantic version ordering, after the base release and before the
next release, and refuses older images. A prerelease base uses Go's corresponding prerelease
pseudo-version form. Release-tag builds keep their exact release version.

`deploy` and `deploy-scan` declare `requires: [deploy-kikd-prod]` in the
repository configuration so planning and jobs stay on owned hardware. Controller
intake seals those requirements before a planner selects the replacement-pin
engine. A detached scan inherits them. Cloud remains for the small smokes that
exercise Cloud itself.

`build-images` builds linux/amd64 and linux/arm64 binaries with `bin/build-release-target.sh` and
publishes them through `publish-images`, reusing the image pair when its immutable version tags
already exist and both revision labels match the candidate commit. A new build checks that both
pinned runtime-base manifests exist before compiling the dashboard or binaries. A missing base
fails with its reference and a repair hint. Reusing published candidate images needs no base lookup.
`staging-gate` pins the
digests in `sparkwing-dev/sparkwing-gitops` and renders with the candidate chart. It pushes the
controller Deployment alone and waits for Argo Synced/Healthy and the rollout, then does the
same for the launcher with every other chart change. Each GitOps push requests a hard
Argo refresh before waiting, requiring patch permission on the named Application.
The candidate CLI compiles alongside staging rollout. Both finish successfully before
either Cloud smoke is submitted. A failure cancels and joins the other operation.
The same job holds staging's concurrency slot through the Cloud gate. Production
can run alongside it. GitOps pushes merge concurrent changes from the other
environment after a rejected push, preserving phase commits for rollback. Argo
may report a descendant of the phase commit only when that environment's entire
GitOps directory is unchanged. Deployment readiness still checks the exact image digest.

The successful staging gate records the candidate CLI's SHA-256 and native platform.
It retains only those executable bytes in a content-addressed temporary directory,
using the deploy scratch expiry. Production copies them into its own private job
directory after checking the gate's source SHA, platform and binary digest.
Missing or mismatched bytes rebuild from the production job's candidate checkout.
A runner without the staging runner's temporary filesystem rebuilds too.
CLI profiles, credentials and stores stay private to each job.

`staging-gate` starts `deploy-smoke` and the `sparkwing-ci-smoke` reference pipeline
before waiting for both on staging Cloud. `production`
requires a staging-gate record for the same digests and deploys the same images. It runs
`deploy-smoke` once in the production team that owns the source repository installation, using
the existing `/kikd-prod/sparkwing/deploy/scan-submit-token` (one NodeClaim, no controller or launcher
WARN or ERROR after both controller and launcher rollouts complete).
Smoke status follows poll every second. NodeClaim sampling interrupts its wait
when all followers finish, then takes a final sample before checking the claims
and logs.
Recovered startup probe failures before readiness remain visible in pod logs but do not
fail the smoke; any warning or error during the smoke still fails it. The QA teams need no installation and are not used for this repository-backed smoke. Staging summarizes its deployed and previous digests before emitting the gate record. Production emits its deployment record. Credentials come
from the run's OIDC token through the `sparkwing-deploy` role in
[deploy-pipeline-iam.md](design/rewrite/proposals/deploy-pipeline-iam.md).

The deploy takes no deploy-time snapshot. Recovery is RDS point-in-time recovery. Production
(`kikd-prod-sparkwing-postgres`, RDS) keeps 7 days; staging (`kikd-prod-sparkwing-staging-postgres`,
a separate Aurora Serverless cluster) keeps 1 day. Take a manual snapshot before a risky
migration. Checking that automated backups are on belongs to external verification through
stormwing, not the deploy. The deploy role has no RDS permission.

After a successful production smoke, the deploy starts a detached child
`deploy-scan` at the deployed commit and digests using `sparkwing.StartChild`.
The child inherits verified-default provenance from the deploy, so the existing
runner trust policy and repository-scoped scan secrets apply; it is exempt from
the parent-completion child sweep. The scan starts the full `example` through the same native
child route and awaits its run ID with `sparkwing.AwaitChild`, retaining the deploy proof and
releasing runner capacity during the wait. The inline scan wrapper runs on the deploy owner.
Example jobs, generated work and lifecycle probes require `deploy-kikd-prod`. Example jobs
share the capacity-one `sparkwing-post-deploy-work` job group. A waiting example job retains
that group while its weather child or serialized probe runs, so other scan jobs cannot fill
the desktop's other slot. Probe children use a separate capacity-one job group to avoid
waiting on their parent's group. The small staging and production smokes retain Cloud placement.
Promotion does not wait for the full scan. All `deploy-scan` runs share the capacity-one
`sparkwing-post-deploy` run group. Queued scans whose source is no longer main's tip stop
before building or running fixtures, so the newest waiting revision wins. The target team's
existing example history is the hourly receipt: an in-flight run suppresses another scan
without waiting or changing findings. A result finished within the last hour is read once
and reported again without starting another example, including failed results.
Staging leaves the full scan for production. Failed example jobs use a fingerprint
containing the environment and job ID. Bitwing promotes each eligible finding to a
ticket after three consecutive breaches or ten total breaches. Later failures append deploy, scan and example run links to that
ticket, preserving the first report. A clean scan resolves unpromoted findings.
Automatic promotion uses Bitwing's eligibility decision and normal `finding promote`;
checks never force promotion.
Finding list supplies IDs; finding show supplies fingerprints and linked tickets.
If per-job status enrichment fails, the known scan failure still publishes a finding
with its run links and the unavailable-details warning. Bitwing policy controls
ticket promotion. Publication receives its full 30-second budget after optional enrichment finishes, including on timeout.
Promoted tickets stay with their repair owner rather than closing automatically.
Bitwing must be installed on that user's PATH. The scan also
requires the runner label and trust conditions documented below.

Before changing production images, deploy reads the latest production scan through
the controller and puts its status and run link in the log and job summary. A failed,
cancelled or unfinished scan warns. Failed jobs that never started are recorded and
ticketed under a separate execution source because their scan feedback could not run.
Pre-planning observations use requested arguments when resolved arguments are
absent. When those arguments are redacted, repository-inherited children of the
`production` job identify production scans; their parent deployment link remains
available. Planning failures with no jobs produce a run-level execution finding. Conclusive
observations without startup failures clear execution findings and reset their
streak; unfinished observations preserve them.
An unavailable observation or ledger warns too. These warnings allow repair deploys
to continue. The check reuses the scan-submit token and starts no runs.

The `deploy-scan` child inherits the parent `deploy` identity for secret selection.
Store `DEPLOY_SCAN_STAGING_TOKEN` and `DEPLOY_SCAN_PRODUCTION_TOKEN` as masked
repository secrets scoped to the `deploy` pipeline, not the child pipeline name. Each value
needs `runs.write`, `runs.read` and `logs.read` in the target deployment's team that owns
the GitHub App installation covering `sparkwing-dev/sparkwing`. Production uses the existing
source-owning team's `/kikd-prod/sparkwing/deploy/scan-submit-token`, also used by its smoke.
A QA fixture token with no installation cannot submit this repository-backed scan. Keep the
source installation bound to its existing team. The optional `--smoke-profile` and
`--smoke-checkout` example probe also needs access to `sparkwing-dev/sparkwing-ci-smoke`;
the detached scan does not enable that probe. Missing prerequisites report that the scan
could not run, rather than clearing prior Bitwing findings.

Each target team must own a GitHub App installation covering
`sparkwing-dev/sparkwing`, because the scan submits a repository-backed example.
A token that can authenticate but belongs to an unrelated QA team cannot submit
that source. An installation belongs to one team. Keep the source-owning team's
installation bound there when repairing a fixture's access.

Replace an ended scan credential in its existing target team, retaining
`runs.read,logs.read,runs.write`, and save it under the same repository and
`deploy` pipeline scope above. Create it from a signed-in account session. A token
delegated from another token cannot outlive its parent. Record the replacement's
expiry and verify authentication and repository access before the next scan.

Before changing production, deploy compares the candidate source's controller schema version with
the previous image's source revision. Missing evidence stops before changing image pins. A failed
production deploy or smoke reverts this run's GitOps commits, launcher first, only if the schema
version did not increase. When it increased, even a startup failure before migration is proven
leaves the candidate pins in place and fails loudly with a roll-forward remedy and a Bitwing
finding. This conservative rule also applies after cancellation and timeout; deploy does not query
the live database to decide whether an older image might still work. No automatic database
downgrade or restore runs. Staging and production have separate database servers.

Bitwing rollback feedback is best-effort until the trusted toolchain copy is provisioned: a missing `/opt/sparkwing-deploy/bin/bitwing` skips ledger publication, retains candidate pins and includes the missing-helper reason in the failed deploy log. It never falls back to the runner's PATH.

Only a run the GitHub App started for a push to main can assume the deploy role. The `deploy`
entry in `.sparkwing/sparkwing.yaml` declares `on: push` for `main`. Separate capacity-one
global job queues serialize image builds, staging rollout plus gate, and production
promotion plus smoke and recovery. A later candidate can build and stage while an earlier
candidate promotes. At build admission and again at staging admission, `git ls-remote`
checks main's tip. Superseded waiters stop before credentials or Cloud smoke at that stage.
Several stale rows can wait, but only the newest waiting commit can pass admission.
Once staging admits a candidate, its rollout and gate finish even when more pushes land.
Before landing this queue-layout change, drain deploy runs and pending intake that
still use the whole-run queue. Their plans do not hold the new stage slots, so
the landing owner must keep the old and new layouts from executing together.
Staging and production fetch main's tip at depth one in shallow checkouts and require the
exact candidate SHA to be an ancestor of `origin/main` or its tip. Ancestry checks first use
available history. A missing proof deepens the descendant's exact SHA by 64 commits, then
fetches full history if the proof still fails. The same check proves that both deployed
image revisions precede the production candidate. Build-time version resolution retains
the release history needed for its stamp.
These fetches preserve `FETCH_HEAD`, which the Cloud smoke uses as repository proof.

Production checks ancestry again immediately before each controller or launcher GitOps push.
A candidate removed by a main history rewrite during deployment preparation is refused at
that write. Source validation and the GitOps push use separate repositories, so this check
does not make them atomic.

Production requires a staging-gate pass for the same image digests and checks both production
image revision labels. Each changed digest must advance from its pinned revision to a newer
source commit, with no version downgrade. Missing revision evidence, unrelated history or a
new digest for the same SHA stops before GitOps pins change. A retry can finish a partial
deployment when a matching digest already runs. An already deployed image pair leaves the
pins unchanged.

A terminal can preview with `--dry-run`. `--stop-after`, `--promote-proven` and `--version`
reach a real deploy through the entry's `args`.

The deployment module's local SDK replacement selects the planning host's
installed executable. Deploy and scan use the owned runner described above;
every job host in a run must retain those exact executable bytes.

A local `pipeline plan` can pass while an older Cloud planning runner rejects the
same source before any job starts. Deploy a wire-contract change to staging and
production before later pipelines depend on it. If that ordering breaks,
bootstrap with an exact image already proven by the staging smokes, compare its
schema with production before changing the controller pin, and wait for its
rollout before updating the launcher and planning runner. Then observe a new
push-triggered deploy through both environments.

#### Delivery timing budgets

The targets are five minutes to land on main, five minutes from main to staging
proof, and five minutes from staging proof to production acceptance. Report
staging rollout separately from staging proof. Proof includes the blocking
smokes. Production acceptance includes its smoke, NodeClaim/log checks and scan
submission, while the detached scan finishes separately.

Every delivery comparison retains the full elapsed leg and a named **cold Cloud
provisioning** segment. Measure that segment from NodeClaim creation to its
successful Initialized transition, with the claim name and both timestamps.
Record each claim once, including capacity used by child and successor jobs.
Land on our own hardware records this segment as `not applicable`. A Cloud leg
without those boundaries records `unknown`, with any observed capacity wait
alongside it. Use zero only when evidence proves the leg required no new capacity.

Include cold provisioning in the full elapsed leg when comparing it with the
five-minute target. Keep image pull, source preparation, execution and observation
tail separate because they have different causes. Component intervals can overlap, so only sum non-overlapping critical-path
intervals. Run acceptance follows preparation and can follow provisioning, so a
controller run-duration headline cannot substitute for the full delivery leg.
Compare cold samples with cold samples and report any provisioning change before
attributing a change to preparation or execution.

Retain measured samples with their raw timing boundaries in the delivery evidence
record. A sample is a baseline, not a timeout or guaranteed provisioning ceiling.
Warm capacity is forbidden for our delivery work.

#### Deploy cost estimate

These are estimates from the code path, not observed deploy timings. The ranges assume a warm
builder, working registry and available Cloud capacity. Queueing and cold compiler caches can
exceed them. The watched staging deploy should record phase durations before setting a speed target.

| Phase | Estimated elapsed time | Bound or main cost in code |
| --- | --- | --- |
| Resolve version, role and existing images | 2-15 seconds | Git fetch and commit metadata, STS, ECR lookups. A matching published pair skips all builds. |
| Dashboard | 30 seconds-3 minutes when rebuilt, seconds on validated reuse | `bin/build-web.sh --reuse` validates its existing input/output receipt before reusing the bundle. |
| Two Linux binary builds | 1-6 minutes warm, 5-20 minutes cold | amd64 then arm64, each compiling the CLI, controller and runner. Whole build job budget: 45 minutes. |
| Publish and verify both multi-arch images | 30 seconds-3 minutes | Controller and runner publish concurrently after all binary hashes pass. One binary layer per platform over pinned runtime bases. Verification reads the published binaries back. |
| GitOps and two rollouts, per environment | 1-8 minutes | Two serial GitOps pushes, each waiting for Argo (10-minute cap, one-second observation interval) and rollout (5-minute cap). |
| Cold Cloud provisioning, per new claim | Measure per leg, no fixed estimate | Creation to Initialized. Included in smoke elapsed time when on its critical path. Report separately from image pull and preparation. |
| Staging gate | 1-10 minutes with available capacity | Candidate CLI build plus two overlapping pipeline runs. Each run has a 45-minute limit. |
| Production smoke | 1-5 minutes with available capacity | CLI build plus two concurrent fixture runs, NodeClaim sampling and log scan. Each run has a 20-minute limit. |
| Production rollback, on failure | Similar to the two rollouts | Two reverse GitOps commits. Budget: 35 minutes. |

The slowest expected work is cold Go compilation, Argo polling and the staging probe runs.
Dashboard receipt reuse and overlapping staging probes are enabled. Reuse by published tag and
revision, skipping unchanged rollout digests, and shared runtime base layers already avoid work.
Different commits still stamp different binaries and revision labels, so a source-unrelated change
does not automatically produce the same image digest.

Parallel architecture builds are a follow-up. Untagged main builds no longer create a shared
Git tag, so measure CPU and memory contention before running them concurrently. Release builds
that create a temporary tag still need coordinated tag ownership. Controller/runner publication
overlaps independent registry I/O and joins both tasks before returning. Persistent Go caches reduce cold builds, while ECR's
existing content-addressed base layers already avoid uploading unchanged runtime layers. An Argo
GitHub webhook can replace polling with prompt sync without widening the deploy role's access.

Job budgets are failure ceilings, not expected duration: build 45 minutes, staging deploy 45,
staging gate 105, and production 100 including rollback. They allow 3h15 for a staging-only path
or 4h55 for all four jobs if every ceiling is reached.

The deploy writes no cluster-scoped object to GitOps: the AppProject `sparkwing` permits no
cluster-scoped kinds, so a GitOps commit cannot grant cluster-wide access. The render drops the
chart's namespaces, ClusterRole, ClusterRoleBinding, admission policies and similar, logs each one
it left out, and fails on any other object without a namespace. Before either deployment phase
pushes GitOps, it reads every rendered ValidatingAdmissionPolicy and binding from the cluster
and fails if any is missing or its spec differs, including the launcher capacity policy. kikd-infra keeps them in
`k8s/sparkwing-cluster/staging.yaml` and `production.yaml`, applied by the Argo application
`sparkwing-cluster` in project `kikd`. An image-only release leaves them alone: the Cloud job
admission policies read the runner and Docker images from the ConfigMap `sparkwing-cloud-admission`
in the job namespace, which the render keeps, so the policies are the same across releases and
rollbacks. A release whose chart changes a rule, the RBAC or the namespaces updates kikd-infra
before its deploy, staging first, then production:

1. Render the chart at the new tag with `helm template <release> charts/sparkwing-full --namespace <ns> -f <env dir>/helm-values.yaml`.
2. Copy its cluster-scoped documents into the environment's file in kikd-infra `k8s/sparkwing-cluster/`. Verify the kustomize output preserves the rendered policy specs, including CEL string values.
3. Land that on kikd-infra main and wait for `sparkwing-cluster` to report Synced.

Add permissions before the deploy and remove them after the old version is gone. That application
prunes, so deleting an object from the file deletes it from the cluster; a Namespace leaves the
file only on purpose, because deleting it deletes everything in it.

```bash
sparkwing run deploy -- --dry-run                         # print every action, take none
sparkwing run deploy -- --dry-run --stop-after staging    # preview a staging-only deploy
sparkwing run deploy -- --dry-run --promote-proven        # preview promoting the last staging-proven digests
```

#### Deploy toolchain

Deploy CLI homes share only the runner's existing content-addressed pipeline
binary cache. Each invocation keeps its profiles, credentials and store in its
private job home. The cache key covers pipeline source and local dependencies,
toolchain identity, GOOS/GOARCH and build flags. Changed inputs rebuild, and
installation verifies the cached bytes before executing a private copy.
If linking the cache fails, submission uses its private cache. Job cleanup
removes the link, while existing cache limits and pruning own retained entries.

Deploys run only on runners labelled `deploy-kikd-prod`. After installing the root-owned toolchain
below, enroll one with `sparkwing runners add --profile prod --runner korey-desktop --session-stdin
--repos github.com/sparkwing-dev/sparkwing --trust verified-default --label deploy-kikd-prod`, using
the account session described in the [runners add docs](docs/cli-runners.md#sparkwing-runners-add).
Local `--dry-run` previews need no runner label.

A real deploy runs `aws`, `kubectl`, `helm`, `git`, `ssh`, `go`, `bash`, `node`, `pnpm` and `bitwing` only from
`/opt/sparkwing-deploy/bin`, and gives every child process `PATH=/opt/sparkwing-deploy/bin:/usr/bin:/bin`.
The directory must be owned by root and not writable by the runner's user, so another job on the same
runner cannot plant a binary that a later deploy runs with production credentials. When the directory is
missing, every deploy job refuses before it fetches a credential; there is no fallback to the runner's
`PATH`. A `--dry-run` preview holds no credentials and still resolves tools from `PATH`. Install the tools
as root, at the versions the deploy machines already use:

```bash
sudo install -d -o root -g root -m 0755 /opt/sparkwing-deploy /opt/sparkwing-deploy/bin
cd "$(mktemp -d)"
# Go, matching go.mod's toolchain line
curl -fsSLO https://go.dev/dl/go1.26.10.linux-amd64.tar.gz
curl -fsSL "https://go.dev/dl/?mode=json&include=all" | grep -A3 '"go1.26.10.linux-amd64.tar.gz"' | grep sha256  # compare
sha256sum go1.26.10.linux-amd64.tar.gz
sudo tar -C /opt/sparkwing-deploy -xzf go1.26.10.linux-amd64.tar.gz
sudo ln -sf /opt/sparkwing-deploy/go/bin/go /opt/sparkwing-deploy/bin/go
# kubectl
curl -fsSLO https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
echo "$(curl -fsSL https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl.sha256)  kubectl" | sha256sum -c
sudo install -o root -g root -m 0755 kubectl /opt/sparkwing-deploy/bin/kubectl
# helm
curl -fsSLO https://get.helm.sh/helm-v4.2.4-linux-amd64.tar.gz
curl -fsSL https://get.helm.sh/helm-v4.2.4-linux-amd64.tar.gz.sha256sum | sha256sum -c
tar -xzf helm-v4.2.4-linux-amd64.tar.gz
sudo install -o root -g root -m 0755 linux-amd64/helm /opt/sparkwing-deploy/bin/helm
# AWS CLI v2
curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o awscliv2.zip
unzip -q awscliv2.zip
sudo ./aws/install -i /opt/sparkwing-deploy/aws-cli -b /opt/sparkwing-deploy/bin
# Node and pnpm for bin/build-web.sh
curl -fsSLO https://nodejs.org/dist/v22.23.1/node-v22.23.1-linux-x64.tar.xz
curl -fsSL https://nodejs.org/dist/v22.23.1/SHASUMS256.txt | grep ' node-v22.23.1-linux-x64.tar.xz$' | sha256sum -c
sudo tar -C /opt/sparkwing-deploy -xJf node-v22.23.1-linux-x64.tar.xz
sudo mv /opt/sparkwing-deploy/node-v22.23.1-linux-x64 /opt/sparkwing-deploy/node
sudo env PATH=/opt/sparkwing-deploy/node/bin:/usr/bin:/bin npm install -g --prefix /opt/sparkwing-deploy/node pnpm@12.5.1
sudo ln -sf /opt/sparkwing-deploy/node/bin/node /opt/sparkwing-deploy/bin/node
sudo ln -sf /opt/sparkwing-deploy/node/bin/pnpm /opt/sparkwing-deploy/bin/pnpm
# Bitwing reporting uses the normal installed binary copied into the trusted toolchain
sudo install -o root -g root -m 0755 "$(command -v bitwing)" /opt/sparkwing-deploy/bin/bitwing
# git, ssh and bash from the root-owned system packages
for t in git ssh bash; do sudo ln -sf "/usr/bin/$t" "/opt/sparkwing-deploy/bin/$t"; done
sudo chown -R root:root /opt/sparkwing-deploy && sudo chmod -R go-w /opt/sparkwing-deploy
ls -l /opt/sparkwing-deploy/bin && /opt/sparkwing-deploy/bin/go version
```

### Check tiers

Current SDK verification runs the Go and TypeScript unit suites and the host
execution scenarios in `TestHostExecMatchesSDKExecution`. Helpers run through the
bound host service routes. Historical SDK acquisition and compatibility matrices
are removed. Old SDK pins require an upgrade to the current contract.

Canonical hosted jobs retain a `canonical-timings` JSON artifact for 30 days
on success and failure. It contains run and step timestamps, outcomes, available
node CPU/memory measurements, and source/workflow identity. It excludes raw
logs, invocation arguments, errors and local paths. Missing measurements remain
null. Pipeline compilation, hosted setup, cache temperature and exact admission
wait are not established by this artifact. A missing handle produces an explicit
unavailable record. Export/upload failures warn without changing the check verdict;
an absent artifact is missing evidence, not a successful export.

Failed canonical hosted jobs retain the full paged `runs logs` output in a
`canonical-run-log-<lane>-<workflow-run>-<attempt>` artifact for 14 days. The
console prints its last 500 lines. A failed log read retains partial output
and its error in the artifact. Missing handles and failed uploads warn without
changing the gate verdict. The artifact contains run output, not the runner's
Sparkwing home, database or controller key.

Pre-release records each check as a sequential WorkStep. A failed check does
not suppress later checks; the parent reports the first failure, while the run
logs and step states retain every individual failure. `--platform-cells` adds
the `macos` and `windows` cells as dispatched jobs requiring `os=darwin` and
`os=windows`; they run the `macos-verify` and `windows-verify` checks. A local
run cannot place them and hosted CI does not pass the flag. Trigger the run on
the team controller; each cell waits visibly for a team runner reporting its
operating system:

```bash
sparkwing pipeline trigger pre-release --profile prod -- --platform-cells
```

Enroll each machine once as a shared team runner scoped to this repository,
piping a dedicated administrator session to stdin, then keep its runner service
running. The service reports `os=darwin` or `os=windows` itself:

```bash
sparkwing runners add --runner build-mac --profile prod --repos github.com/sparkwing-dev/sparkwing --max-concurrent 1 --session-stdin
sparkwing runners add --runner build-windows --profile prod --repos github.com/sparkwing-dev/sparkwing --max-concurrent 1 --session-stdin
``` The canonical timing
artifact retains those step outcomes and durations but excludes their error text.

Pinned actionlint v1.7.12 does not recognize GitHub's `concurrency.queue`.
Its config ignores only that exact diagnostic in `release.yaml`. The workflow
contract tests require both publication jobs' shared group, `queue: max`, and
`cancel-in-progress: false`, and reject uncovered queue use elsewhere in that
file. Other syntax and workflow checks remain active.

- **The three tiers:** `pre-commit` judges the staged change against this
  repo's source policy and nothing else; the git pre-commit hook runs it.
  `pre-push` is the fast tier the git pre-push hook runs, and it repeats
  nothing the commit tier already ran: the changelog, OpenAPI and API-snapshot
  gates, and `go build`, `go vet` and the fast linter subset over the packages
  the push touches, with no package-count cutoff. The linter includes the
  depguard dependency rules. A
  runner reserves the combined CPU demand of those three concurrent compiler
  tasks, up to 9 cores. Smaller runners divide their available cores across
  the tasks.
  The tiers shift left, so a
  push whose commits skipped the commit hook is judged by `gate` rather than a second
  time here. Rebase replay does not fire pre-commit, and automatic merges use
  pre-merge-commit; neither hook tier proves source policy over those resulting
  trees. Run `gate` when that evidence is required. Everything
  else is `gate` (the broad check) and `pre-release` (the release boundary),
  which `sparkwing run <name>` runs on demand; no git hook fires either. Hosted
  CI verifies, it does not gate (decision 96): every pull request and push to
  main runs `gate` and `race-touched` over the change since the push's previous
  commit (or the pull request base), and the full suite, `pre-release`, the
  store race lanes and Postgres conformance run nightly and on dispatch.
  The full acceptance suite includes `TestFeatureE2E`, which exercises placement
  deadlines, failed-only retries with outputs and artifacts, and child-run
  cancellation against isolated local controller processes. Run just these with
  `sparkwing run feature-e2e --sw-route local`. Its `--scenario` and `--repeat`
  arguments select one scenario or repeat fresh environments. The
  [acceptance runbook](tests/acceptance/README.md#feature-scenarios-against-a-local-controller)
  names the real-time budgets, trust setting and runner-actor boundary.
  `sparkwing pipeline hooks install` arms the two hooks in a checkout and
  `sparkwing pipeline hooks status` checks the active hook directory and compares
  each hook's presence, content, format version, and executability. Blocking
  gates are required; notifiers are optional. Status reports that verdict for
  the current repository. Installation writes hooks without running pipelines
  and refuses a conflicting `core.hooksPath`; compose hooks manually or clear
  the reported override. Reinstall stale or missing hooks; use a throwaway
  commit when a manual execution check is needed.
  A hook script names its pipeline, so a checkout whose hooks
  were installed when the pre-push hook ran `gate` keeps running `gate` until
  `sparkwing pipeline hooks install` rewrites them.
- **The three check classes and their budgets:** `pre-commit` is the source
  policy at 3 seconds, `pre-push` the fast tier with a one-minute budget,
  and the release cut, the `release-cut-checks` job the `release` pipeline runs
  before it tags, 40 minutes including 30 minutes of lint-lock allowance
  and ten minutes for build, full lint, the short test suite,
  published-version, Go-directive and SDK-pin checks, and changelog-link and docs-sidebar checks
  in parallel.
  Each of those jobs times its own steps and warns when its slowest step's
  own run time overruns the class, naming the step and its cost, so a class
  cannot regrow unnoticed. A tier whose checks all passed still passes: when
  the budget failed green tiers, authors under machine load committed with
  `--no-verify`, which skips every check, and lands then found the policy
  failures twenty minutes later.
  A tier whose scope holds no Go file still runs and passes every step, so it warns and
  names the scope it read: a push judged against an empty
  `origin/main..HEAD` range reports success over nothing, and uncommitted
  work is outside what `pre-push` reads. The
  budget judges the slowest step's own run time, not the admission wait, the
  time a step waits for a core or a step slot, or the 2.5 s the pipeline
  binary takes to recompile after a Go change. On a host at load 28, a
  pre-commit whose slowest check took 473 ms spanned 3.8 s and failed the
  old span budget. The verdict line reports both numbers. The
  formatters are the per-file cost in the commit tier, and `goimports` inside
  `golangci-lint fmt` is all of it: over a hundred Go files `gofumpt` measured
  0.38 s, `goimports` 10.9 s and `gofmt` 0.09 s, so a Go file costs about
  0.11 s. `gate`
  and `pre-release` are the heavier classes: they carry no performance budget
  and run asynchronously, on demand and in hosted CI. The broad gate declares
  separate 70-minute execution deadlines for broad checks and touched-package
  race tests. Each gets 71 minutes with the dispatcher's drain margin, inside
  its own 80-minute hosted job. Hosted CI selects them with `sparkwing run gate
  --sw-only gate -- --full` and `sparkwing run gate --sw-only race-touched`. Each job
  owns a separate four-core runner, so race tests share no cores with the full
  Go or PostgreSQL suite. `--sw-only` keeps a job's dependencies, so each lane
  runs the `policy` job first; on a fresh runner its first compile of the
  checkers and the CLI is the cost. `sparkwing run gate` runs the policy job,
  then both jobs, locally. There
  both keep the one-core admission charge while running half the runtime's
  CPU budget (the smaller of the machine CPU count and `GOMAXPROCS`), overcommitting like fixed runner slots; local admission counts their
  CPU as Sparkwing load, not external. The `preconditions`, `review`,
  `record-tree` and `land` jobs declare a quarter core (`Cores(0.25)`), so they
  start beside running gates instead of queueing behind them. A simulation of
  six concurrent lands found declaring 4 or 6 cores for the gate pair slower
  than this under external load.
  The `cli-docs` policy step compiles the CLI's registry, help metadata and
  Markdown renderer through `reference_generate.go`, without linking the
  execution runtime or embedding the docs it checks. `gen-cli-docs.sh --check`
  still regenerates every reference page and rejects changed, missing or stale
  generated pages. `TestCommandReferenceMatchesRegistry` also compares the
  committed pages with the renderer compiled into the full CLI.
  Without `--full`, vet and build cover the affected dependency closure. Its test step
  runs `go test -short` on changed packages
  and their reverse dependency closure, including production, internal-test
  and external-test imports. The critical set runs separately without
  `-short`, including critical tests in affected packages. A `go.mod` or
  `go.sum` change runs its whole module's fast class and critical set.
  A store schema change selects `internal/store` and its importers.
  Long integration bodies guarded by `testing.Short()` and the PostgreSQL
  suite run after deploy on our hardware with
  `sparkwing run gate --sw-route local -- --full`, and remain in full nightly
  and pre-release checks. The deploy scan trigger must invoke that command
  outside the land and deploy critical paths. A
  file outside every package runs the packages whose tests read it, from the
  `nonPackageReaders` table in `.sparkwing/jobs/affected_tests.go` (`.github/`
  and `bin/` run `.sparkwing/jobs`, `docs/` runs `internal/doccheck` and
  `cmd/sparkwing`, and so on); `CHANGELOG.md` and generated `pkg/docs/` Markdown and schema mirrors stay with the docs-mirror
  and changelog steps. File readers select their own tests without charging
  importers whose code did not change. Changes under `pkg/wingwire/`, `internal/controllerwire/`,
  `internal/apispec/` or `api/` also run `.sparkwing/jobs`, including
  `TestRepositoryUnreleasedSectionDeclaresEveryWireCut`, because the release
  wire-cut check reads the protocol and API surfaces outside its package.
  Lint starts independently of the test step because it has no test-result
  dependency. Affected lint runs the full configured linter set with bounded
  workers and its existing checkout cache, without waiting on the global
  serial-runner lock. Full lint still waits for that lock. The real lint
  integration fixture stays in the full test class. Fixture-module dependency
  changes do not widen product lint. Its lint step lints only the packages holding changed Go
  files (every package when `go.mod`, `go.sum` or `.golangci.yml` changed),
  since `--new-from-rev` drops findings outside the change anyway. With
  `--full`, race tests cover every package but `internal/store` and the
  PostgreSQL suite always runs, so a full pass does not depend on the
  producer's `origin/main`. The broad job's 70-minute deadline already allows
  the full Go suite and the 30-minute PostgreSQL limit; hosted CI runs the race
  job without `--full` for pushes and pull requests, so it runs the fast race
  class on touched packages. Local post-deploy scans pass `--full` to retain long race tests across
  every package except the deferred store.
  Affected race runs use the stable private test home and native Go test cache,
  without `-count=1`. Each stable suite home is locked through setup, execution
  and cleanup, so overlapping runs of the same step in one checkout wait for
  each other. Full race runs keep the uncached shard path. The Go build
  cache already survives runs, and hosted jobs restore and save it through
  their existing cache steps. No successful policy or review check is skipped
  by a package cache hit. After
  both jobs run and pass on a clean checkout, `record-tree` outputs HEAD's tree, the
  `.sparkwing` definition fingerprint, the mode and the engine version. Each
  gate job reads that output through a `sw.FromProvenRunAtCommit()` reference
  and skips itself, naming the runner that proved it, when the controller finds
  a full-mode pass at the same commit from a run proven for this repository
  (from GitHub, or fetched by the controller) that selected every job without
  dry run, sealed no environment variables and ran every job on a trusted host
  (a fresh host, or a runner scoped to repositories with `--repos`), and that pass names the same tree, fingerprint and engine.
  `record-tree` reads both jobs' in-run outputs and records nothing when
  either skipped, and the lookup passes over a run whose `record-tree` did not
  succeed, so a run that skipped everything neither records a pass of its own
  nor hides the older pass it skipped on. A plain
  local run proves no repository, so it never lets another run skip;
  `sparkwing pipeline trigger gate --profile P --trusted -- --full` runs the
  same commit on this machine's repository-scoped runner instead, which can. The
  existing required `Canonical / gate` check succeeds only after the broad,
  race, pre-release and three `race-store-k-of-3` matrix jobs all succeed. A
  skipped, cancelled or failed matrix cannot pass that check. Pre-release
  declares a 120-minute execution deadline inside a 130-minute hosted job,
  leaving ten minutes beyond the node deadline for setup and cleanup; the
  race-store lanes run the same pipeline and keep the same deadlines.
  Explicit `source_ref` or `release_tag` overrides run that source's complete
  gate graph in one hosted job, because older refs do not declare a separate
  race job. Normal pull requests and pushes use the split jobs.
  These are liveness boundaries, not claims that either check normally takes
  that long. A failed hosted canonical run gets
  two minutes to print its stored status and the last 500 log lines from the
  run handle. On exactly four logical CPUs, the ordinary race command can run
  two package binaries while each keeps
  `GOMAXPROCS=1`. When touched and the host has capacity for two package
  processes, `internal/controller`, `internal/nodehost`, `cmd/sparkwing`,
  `internal/wingd/localcontroller` and `tests/acceptance` run after the
  module's ordinary packages. Measured long poles get a shard of their own,
  queued ahead of every round-robin shard: `TestStorage` in acceptance, and
  `TestLogTreeTraversesEveryTenantDescendantProperty` and
  `TestRetryHistoryTraversesLongChainsAndCycles` in controller. Controller
  splits its remaining tests into six round-robin shards and acceptance into
  three. Nodehost and the CLI use two. Localcontroller keeps its full suite in
  one queued process because it fits the package budget. A lead test that is
  no longer listed fails the step, so a rename cannot silently drop it from
  the plan. The runner compiles and lists each package once, then queues every
  selected package's shards through the host's package-process allowance:
  four workers immediately on the dedicated hosted four-core runner, two
  workers on a shared four-core host, and the existing Go step parallelism on
  larger hosts. A free worker can start the next package while
  another finishes a long controller shard.
  Each shard keeps `GOMAXPROCS=1` and runs its assigned tests once, including
  the slow class. Listing, coverage checks, failure cancellation and output
  collection use the same runner as the store's release shards. The runner
  logs the sorted list's count and SHA-256, plus each shard's count and hash,
  and rejects duplicate, missing or multiply assigned names.
  `race-touched` has its own 70-minute execution budget. Ordinary
  package processes and shard processes have an explicit 52m30s timeout,
  derived as three quarters of that budget. This raises the ordinary package
  timeout from 45 minutes and replaces nodehost's ten-minute shard timeout.
  The race node deadline bounds its builds, listing and every selected package.
  Broad checks have their own deadline. Controller exceeded the former unsharded
  45-minute timeout on hosted CI without a hung test. The unsharded one-thread
  local nodehost baseline took 687 seconds. Parallel tests within a runtime
  share the same CPU bound. Hosts with capacity for one package retain the
  ordinary race command. On a four-core single-tenant host, the ordinary
  command uses one-thread, two-package bounds on its separate runner, and the
  PostgreSQL suite runs four one-thread shard processes. Fitting each hosted deadline and improving cold-start
  latency still require measured evidence. The full Go suite uses a 25-minute per-package timeout;
  hosted runs 36678720566 and 36689529462 hit Go's default 10-minute timeout
  in `internal/controller` or `internal/store` while the active tests had run for 0s.
  Lint and the PostgreSQL suite, conditional outside `--full`, wait for the full Go suite.
  Touched-package race tests run as an independent job on every host. Existing
  per-command Go parallelism stays unchanged.
  `internal/store` is not among the gate's race targets. Its full race suite
  exceeded 80 minutes in every one of four shards on the hosted runner, so
  adding it to the gate would restore a check that cannot pass.
  `pre-release` runs it instead, as `race-store`, and no release ships past it.
  That step compiles one race-enabled `internal/store` test binary, lists its top-level
  Test, Example and Fuzz names, sorts them and assigns each exactly once across
  four processes per part, longest first onto the least loaded shard using the
  measured costs in `.sparkwing/jobs/store_race_costs.txt`. A name the table
  does not know costs the mean of the names it does, and a stale line is
  ignored. `--race-store-part k/n` (pipeline input) computes the same
  assignment over `4n` shards on every runner and runs only part k's four;
  the default runs all four shards of one part. On normal pull requests and
  pushes, hosted CI runs `--sw-start-at race-store --sw-stop-at race-store --
  --race-store-part k/3` in three `race-store-k-of-3` lanes, each on its own
  four-core runner, and the pre-release lane passes `--race-store-part none`,
  which runs no shard. `TestCanonicalStoreRacePartsCoverEveryPartOnce` fails
  unless the lanes cover parts 1 to n exactly once whenever pre-release passes
  `none`. Source and release-tag overrides keep the whole store race inside
  their one pre-release job. The store race runs without PostgreSQL: a job's
  environment never inherits `SPARKWING_TEST_*` names, so the PostgreSQL store
  tests skip under race and run unraced in `store-postgres` against its own
  embedded server. Every shard runs with `GOMAXPROCS=1`; the separate
  `internal/store/internal/storetest` package runs once, in part 1. The step stops sibling
  processes on a failure and retains each shard's output in the run log. It
  gives the suite a fresh `SPARKWING_HOME`, clears inherited runner credentials
  and service bindings, and passes `GOWORK=off` into Go commands in worktrees.
  A complete local run on 16 logical CPUs covered 1,075 top-level names in
  27m20s; its four shards took 21m45s, 23m15s, 23m40s and 27m20s. A prior
  pre-release run without this step took 13m03s, so 40m23s is a local estimate
  for the whole release check, not a hosted measurement. On hosted four-core
  runners, pre-release reached `race-store` after about 8 minutes in one run
  and 18 minutes in another. All four shards reached the former 55-minute
  timeout with different active tests running for 1s to 6s. The new 80-minute
  shard timeout is a hypothesis to verify on hosted runners, not a measured
  passing budget. Hosted run 40856721261 still ran all four shards past it,
  with TestRefillModel alone at over a minute under race, so the shards run
  the short class (`-test.short`); the tests that guard themselves as slow keep
  running without race in `gate` and on PostgreSQL in `store-postgres`. Later checks remain under the 120-minute node deadline and a
  130-minute hosted job deadline.
  **Store race costs, 2026-11-03:** hosted runs 41578268161 and 41590717553
  each spent about 82 s on `storetest`, 71 s compiling
  and listing 1,860 names, then four round-robin shards of 54m51s to 64m51s.
  A local run at c6dff62df (four shards, `CI=1`, `GOMAXPROCS=1`, pinned with
  `taskset` to four cores of a loaded 16-core host) took 28m11s to 31m49s per
  shard over 1,862 names, 7,096 s of test time in all, the longest test 189 s.
  Hosted shards ran 1.93 to 2.13 times their local cost, about 2.03 on
  average, so the suite is about 240 hosted core-minutes. Round-robin's
  slowest shard held 7% more than the mean; cost-balanced shards land within
  a second of each other on the measured table, so balancing alone saves
  about four hosted minutes of 65. On the same four pinned cores, the
  balanced four-shard assignment ran 29m52s to 30m38s against round-robin's
  28m11s to 31m49s, and the pipeline's own part 2 of 3 took 11m21s
  including pipeline and race compilation. Three parts of four shards hold about
  591 local seconds, about 20 hosted minutes, per shard. Projected per lane:
  about 2 minutes of setup, 2.5 minutes of pipeline and race compilation and
  `storetest` in part 1, and 20 minutes of shards, about 25 minutes, under
  the broad gate's 32-minute hosted job. Pre-release without the store race
  takes about 20 minutes: setup, the other checks (10 minutes in run
  41590717553), and `store-postgres`, which took 7m25s locally on four pinned
  cores of the same loaded host. With the store race inside it, the same job
  measured 6,000 s before the PostgreSQL rewrite and projects to about 85
  minutes after it. The CI workflow's critical path
  moves from pre-release (100m09s from creation to completion in run
  41590717553) to the broad gate, about 33 minutes. Runner time rises by
  about 10 minutes per run, from about 85 to about 95 four-core minutes
  across pre-release and the race-store lanes, for setup and compilation on
  three more runners. These are projections from the timings
  above until a hosted run measures them.
  The hosted broad and touched-package race
  jobs each keep an 80-minute deadline.
  A Go step's own parallelism depends on who else holds the
  box. A shared host bounds each step to `(cpus-1)/2`, so one gate cannot
  saturate a machine another gate is running on. A host that sets `CI` carries
  one gate and is discarded after it, so it holds nothing back for a neighbor
  and bounds each step to `cpus/2` instead. The two agree on an odd CPU count
  and differ by one thread on an even one, except where the shared form
  collapses: at four CPUs a shared host runs one thread and a single-tenant
  host runs two. The race suite keeps `GOMAXPROCS=1`
  on four CPUs either way.
  A local four-CPU `CI=1` run using the hosted schedule (202014, source
  f314ad61) finished full Go in 23m18s before PostgreSQL began. Three measured
  PostgreSQL store runs took 16m24s, 16m45s and 20m15s. Those two serial
  checks alone project to 39m42s–43m33s, excluding build, lint and cleanup.
  Hosted source `0f118be32` took 35m32s before PostgreSQL started: 31m20s in
  the full Go step after build and setup. The 50-minute node deadline then
  cancelled PostgreSQL after 14m31s, before its 25-minute Go timeout. The
  server was still writing WAL near cancellation. The 70-minute node deadline
  allows that measured prefix, the full 30-minute PostgreSQL limit, and
  cleanup. A passing hosted total at this deadline remains unmeasured.
  That boundary remains a liveness limit rather than a completion
  guarantee. That publication path
  stops at the parent runner rather than
  entering node processes, so nested Sparkwing commands choose their own
  output. Measured on this 16-core Linux
  host with a warm cache at source `2eefff146`, the release cut's three
  members cost 17.001 s (build), 73.455 s (full lint), and 439.746 s
  (`go test -short`). Every check passed; the span exceeded the former
  five-minute budget. `internal/store` took 361.767 s inside that run. A focused
  `internal/store -short` run without concurrent build or lint took 347.558 s,
  with 1,214 top-level passes and a 240 ms median test duration; 1,130
  exceeded 200 ms. Repeated current-schema SQLite setup remains in this
  coverage. The ten-minute check allowance keeps the entire short suite and its
  checks, with room above the measured 7m19.746s class span. `sparkwing runs stats
  --pipeline pre-commit --since 7d` reports what a class has cost over the
  week, and `sparkwing runs status --run <id> --view timeline --steps` breaks one run into
  its steps; the runs store is shared across repositories, so filter the runs
  by repo before reading a per-pipeline figure as this one's.
- **The short test class:** the release cut runs `go test -short`. Use
  200 ms as guidance for expensive test-body work beyond current-schema
  fixture setup. Such work should guard itself with
  `if testing.Short() { t.Skip("slow: ...") }` naming what costs the time.
  Real-store fixture setup remains in the short suite; the measured package
  and test durations above include that setup. The short class retains a
  real-store risk-admission test that requires
  an unapproved submission to leave the queue empty. First-run compilation,
  ref selection and pinned-binary risk fixtures remain in the full class.
  `gate -- --full` runs the suite without `-short`, which is where every
  guarded test is still judged. Affected landing gates use the short class. On 2026-10-29, with the cut's `GOMAXPROCS=7 -p 7` on a shared
  16-core host at load 9 to 15, `cmd/sparkwing`, `internal/controller` and
  `internal/store` took 241 s, 832 s and 617 s under `-short`, two of them past
  Go's ten-minute package limit. The tests that measured 1 s or more there
  now split by what they guard. A `testing/quick` property keeps running under
  `-short`, checking three fixed-seed cases through `internal/quicktest`; a
  test whose name or file names an authorization, fence, expiry, masking,
  tenant or secret boundary keeps running whole; the rest skip as slow. That
  brought the three packages to 123 s, 265 s and 353 s at load 6 to 7.
- **What the two hooks cost:** measured on this 16-core Linux host. `pre-push`
  retains a ten-second target when the compiler cache is warm. Run records
  expose total and per-step durations, but they carry no compiler-cache
  temperature signal, so the target is measured rather than enforced. A warm
  integrated run over 13 changed Go files took 10.678 s and missed the target;
  fast lint took 10.678 s and build took 9.782 s. A controlled cold run of the
  same functional checks took 24.432 s, and broader cold parallel samples took
  51.182 s and 56.923 s. These observations fit the one-minute budget but
  do not establish a ceiling for every cold input. A change to a Go file also
  invalidates the cached `.sparkwing/` pipeline binary, so the first hook run
  after one pays about 2.5 s to recompile it outside the timed job span.
- **Gate and land speed fixes, measured 2026-11-04** on this 16-core host at
  load 13 to 35 (raw data in the sweep's `speed-data/ci/fixes/`).
  Hosted CI: a per-commit Go cache key saved from main pushes took the CLI
  build from 112 s cold (99 s median on the stale cache) to 6 s plus a 15 s
  restore on gate-core, 74 s to 15 s plus 26 s on race-touched, and vet from
  297 s cold (161 s median) to 6 s; each save is about 930 MiB. Locally, with
  the lint cache in the tree `sparkwing pipeline list` took 61 to 75 s and
  with it in the git directory 2.5 s. A cold whole-module lint of the root
  measured 551 s; a change outside the root module now skips it, while a deep
  package (`internal/bincache`) still took 465 s cold because golangci-lint
  loads every dependency. A stable test home made `internal/paths` and
  `internal/bincache` replay from the cache on a rerun; `cmd/sparkwing`
  (400 to 630 s) still reruns. Land's push skipped a pre-push run that had cost
  37 to 632 s. The old `gofmt -l .` failed 2 of 150 runs beside a looping
  `npm ci`; the pruned walk failed none.
- **Which tier decides a merge:** the hooks judge what they can in seconds and
  nothing more, so main may go red. Follow the [release failure policy](#release-failure-policy).
  Main is where the broad checks run:
  `gate` and `pre-release` run in hosted CI on every pull request and every
  push to main. A tag re-runs nothing, so what main is green for is what ships.
  Run `sparkwing run gate` yourself when a change is broad enough that a hosted
  red would cost more than the wait.
- **What a test step inherits:** every step that starts a product suite
  (`test`, `race-touched`, `store-postgres`, and the release contract
  preflight) clears the bindings `internal/runners/local/env.go` injects into a
  node child and pins `SPARKWING_HOME` to an empty directory of its own, which
  holds no `dev.env` for a CLI verb to find a dashboard through. The gate's
  `test` step, the short suite and the `test` pipeline key that directory by
  checkout and step under `$TMPDIR` and empty it before and after each run, so
  `go test` can replay a passing package that reads the home; a fresh path per
  run defeated its cache. Every product test step also pins
  `SPARKWING_TEST_REPO_STATE` (HEAD's tree plus a hash of uncommitted changes
  and of untracked files' names and contents), which the pipeline module's
  `TestCheckoutStateKeysTheTestCache` reads: go test never hashes files above
  the module and records a variable only while a test runs, so without it a
  change to `.github/` or `bin/` would replay that module's cached pass. A gate runs inside a sparkwing node, which hands its children the
  machine's admission socket, the dispatcher's service URLs and the run's own
  credentials, so a suite that read one would reach a live service and fail
  only under the gate. The node consumes its WorkStep window before the body
  starts, so a nested Sparkwing command selects its own steps. A variable that
  injector gains and the scrub does not handle fails a contract test in the
  pipeline module.
- **Why the whole-tree vet, test and lint are in neither hook:** the house
  standard puts them in the pre-commit chain, and this repo runs them in `gate`
  on purpose. Earlier broad-tier runs took 12 to 24 minutes through the shared
  admission daemon. At that time, the Postgres suite took 401 s, race tests
  401 s, the full unit suite 315 s, and lint 117 s. The Postgres lane now takes
  693 s end to end. No new broad-tier total has been measured. A hook that
  long is a hook everyone passes `--no-verify`, and it loses the fast-forward
  race whenever a co-maintainer lands first. Hosted CI runs `gate` and
  `pre-release` on every pull request and push to main, so a landing pays for
  them there. What the push tier keeps of the four is the packages the change
  touches: `go build`, `go vet`,
  and the fast linter subset, the whole-tree linters minus the type-and-SSA
  family, which costs minutes. Every touched package is checked even on a
  wide push; the file-count waiver relaxes only elapsed time. No test suite
  runs in a hook tier:
  the fast test class belongs to the release cut, and the hooks run formatters,
  sweeps, contract gates and linters. The suites are the wrong shape for a
  ten-second tier -- the short suite of `internal/store` alone measured 53 s,
  because several hundred of its tests each cost between 50 and 200 ms in
  fixture setup.
- **Cheap:** format touched Go files and run the affected package tests, for
  example `go test ./internal/orchestrator -run RunAndAwait`. The `lint`,
  `test`, and `build` pipelines are focused checks when their whole boundary is
  relevant; invoke one with `sparkwing run <name>`.
- **Experimental semantic lint:** `sparkwing run jev-lint` compares changed
  non-test Go functions with deterministically selected candidates across the
  repository. It also reads `.sparkwing/jev-invariants.yaml`, selects invariants
  whose scopes contain changed files, and asks one independent Jev question per
  invariant over a shared bounded diff. Jev judges those contracts, duplicate
  responsibility and package ownership, unnamed complex conditions,
  unexplained magic values, and whether a large change builds a mechanism
  around a reversible premise instead of fixing the cause. Cross-package
  findings may recommend either existing package or a focused shared package.
  Store the API key as the masked `TYPESAFE_API_KEY` Sparkwing secret. Use
  `--dry-run` to inspect every bounded request without a key or network call.
  Jev skips symlinked Go candidates and refuses changed files or invariant
  config reached through a symlink, so a request reads only checkout files.
  Findings are advisory; configuration, analysis, transport, and
  response-schema failures fail the run. Cached exact requests can be read
  without the key.
- **Whole-codebase semantic sweep:** `sparkwing run jev-sweeper` is a manual,
  heavier companion to `jev-lint`. It parses every non-test Go function,
  combines high-signal ranking with a reproducible shuffled sample across
  packages, and batches independent Jev judgments for mixed responsibilities,
  Open/Closed and dependency-inversion violations, abstraction-boundary
  leakage, misleading contracts, primitive obsession, feature envy,
  unnecessary indirection, and duplicate responsibility. The shuffled sample
  rotates daily by default; `--shuffle-seed` reproduces it. `--max-functions`,
  `--max-pairs`, `--sample-functions`, and `--sample-pairs` control breadth;
  `--dry-run` prints every request. Findings remain advisory and do not replace
  deterministic gates.
- **Scripts under `bin/`:** editing one means running `GOWORK=off go test
  ./bin`, about ten seconds. Go tests there pin what the scripts do, including
  the exact argv `bin/install.sh` builds with, so a change to a shell script
  reds a package no other check points at.
- **Orchestrator iteration:** `GOWORK=off go test -short
  -timeout=5m ./internal/orchestrator` keeps the inexpensive `RunLocal` and
  daemon coverage. It skips the process-per-node binary fixtures, scaffolded
  headless module, and tests that exercise sustained contention or real
  timeout windows. Run without `-short` when changing those boundaries. The
  normal `test` and `gate` pipelines retain those tests.
- **Goroutine leaks:** every package with tests carries a `leak_test.go` whose
  `TestMain` hands the suite to `internal/testleak`, so a package fails when a
  goroutine outlives its tests. A new test package needs that file too; copy an
  existing one. A package that must tolerate a goroutine passes its own
  `goleak` option from its `TestMain` and says why in a `safety:` comment. A
  helper process a test spawns by re-executing the test binary runs without the
  check, so a goroutine such a helper leaves behind fails nothing; the check
  covers the suite, not the children it starts. The same package moves HOME,
  SPARKWING_HOME and XDG_CONFIG_HOME into a private sandbox. Before it does,
  it puts the suite's own `go env GOROOT`/bin first on PATH and pins asdf and
  mise to the developer's real data dir, config and tool versions
  (`ASDF_DATA_DIR`, `ASDF_<TOOL>_VERSION`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`),
  so `go` and `helm` started through a shim still resolve. A variable already
  set is left alone.
- **Frontend dependencies:** the gate's `frontend-deps` step runs
  `pnpm --dir web install --frozen-lockfile --prefer-offline --ignore-scripts`
  before the frontend steps and the Go `test` step, whose `tests/acceptance`
  drives the dashboard client through `web/node_modules`. It is a no-op when
  `web/node_modules` matches the lockfile. The affected gate skips it when the
  change touches no frontend input, unless `web/node_modules` is missing, and
  the land re-check after merging `origin/main` installs a missing tree before
  its tests. Without pnpm and without `web/node_modules` it fails naming that
  command.
- **Go result caching:** ordinary `test` and `gate` checks preserve the
  caller's temporary-directory settings so Go can reuse passing results.
  Test fixtures own cleanup through `t.TempDir`, `t.Cleanup`, or deferred
  removal. Forced race and Postgres runs retain their per-run scratch roots.
- **Single-core race shard costs:** measured on a 16-logical-CPU Linux host
  with Go 1.26.10 during concurrent builds and tests. The load average exceeded
  24, so these are contended local samples. Controller used four shards and
  two workers, with commands of the form
  `GOMAXPROCS=1 go test -race -count=1 -run "$pattern" ./internal/controller/`,
  with each pattern selecting one sorted round-robin shard. The CLI used
  compiled race binaries with `GOMAXPROCS=1` and `-test.count=1`.
  Localcontroller used `GOMAXPROCS=1 go test -race -count=1` on the whole
  package. Every measurement ran the full test class.

  | Package | Listed names | Shard seconds, in order | Result |
  | --- | ---: | --- | --- |
  | `internal/controller` | 1,026 | 1040.73 / 1884.44 / 953.24 / 949.81 | All four passed |
  | `cmd/sparkwing` | 1,129 | 950.51 / 975.64 | Both passed |
  | `internal/wingd/localcontroller` | 21 | 156.63 | Whole package passed |

  Two preliminary controller shards each hit a 35-minute measurement guard,
  after 508 and 263 of their 513 names, respectively.
  A four-shard trial with a 30-minute guard completed shards 1, 3 and 4, and
  stopped shard 2 after 245 of its 257 names. The table uses shard 2's complete
  rerun with the configured 52m30s timeout. Together the final four samples
  cover 257, 257, 256 and 256 names, with no skips. Controller's sorted listing
  SHA-256 was `dc830a363f175b8cf17c1eb91083e7d1061f23936921989100ee5fd917b17770`.
  CLI compilation took 126.75s separately from its shard timings.
  An exploratory two-process localcontroller run compiled in 37.62s,
  took 23.58s and 159.86s in its two processes, and reported
  `unstarted local renewal changed claim` in
  `TestLocalControllerTwoJobProcessesUseIndependentAwards`. The final
  one-process sample passed all 21 names, so the queue keeps this package
  together. Full hosted batch completion remains unmeasured.

- **Touched-package race costs, 2026-11-01:** hosted run 41330942938 raced
  six packages in one `go test -p 2` call and the gate job hit its 70-minute
  deadline: `tests/acceptance` alone took 3150.5 s there, so the controller
  shards could not start until minute 61. Local samples below ran on the
  16-logical-CPU Linux host with Go 1.26.10, each package as
  `GOMAXPROCS=1 go test -race -count=1 ./<pkg>` with a private home, six
  packages at once beside other lanes (load average 4 to 12).

  | Package | Local wall | Local CPU (user+sys) | Hosted wall (41330942938) |
  | --- | ---: | ---: | ---: |
  | `internal/controller` | 5112.8 s | 4724.8 s | not reached |
  | `internal/controller/client` | 5.0 s | 4.0 s | 3.0 s |
  | `internal/controllerwire` | 2.1 s | 1.0 s | 1.0 s |
  | `internal/runners/launcher` | 102.6 s | 100.3 s | 205.5 s |
  | `internal/source` | 37.2 s | 35.1 s | 70.0 s |
  | `tests/acceptance` | 2408.2 s | 2345.6 s | 3150.5 s |

  Both long packages are CPU-bound under race, so splitting them only helps
  when a core is free. `TestStorage` took 1696.6 s of acceptance's total; a
  four-way round-robin split of acceptance ran 1990.3, 257.8, 173.1 and
  202.1 s, the long shard being the one holding `TestStorage`. Controller's
  two property tests above took 663.9 and 604.2 s; split ten ways, its
  groups ran 291 to 1145 s, the two longest holding those tests. The hosted-to-local ratio was 1.31 for
  acceptance and about 2.0 for launcher and source.

  Later the same day the three long poles got cheaper under race, measured
  the same way on a quieter host (load average 4 to 6). Fixture inserts in
  the controller tests became one statement each, because every
  `teststore.Exec` reparses the schema and reprojects every run. Under race,
  retry history reads its 270-link chain through one selector per shape
  instead of three, and `TestStorage` checks the first 16 of its 128 depcache
  cases per backend. The full suite without race still checks every selector
  and all 128 cases. Test rows ran each test alone with `-run`.

  | Test or package | Race before | Race after | No race before | No race after |
  | --- | ---: | ---: | ---: | ---: |
  | `TestStorage` | 1681.7 s | 271.0 s | 177.9 s | 148.5 s |
  | `TestLogTreeTraversesEveryTenantDescendantProperty` | 585.9 s | 65.3 s | 99.8 s | 7.7 s |
  | `TestRetryHistoryTraversesLongChainsAndCycles` | 564.7 s | 95.1 s | 95.6 s | 12.9 s |
  | `tests/acceptance` | 2408.2 s | 890.5 s | | |
  | `internal/controller` | 5112.8 s | 3836.4 s | | |

  The package CPU (user+sys) after the change was 873.7 s for acceptance and
  3680.9 s for controller. Retry history still costs about 6 s per page under
  race because every page request rewalks the whole retry tree.

  Projected plan on the former shared hosted runner, from the measurements
  before those reductions, for a batch touching all six packages, at the 1.31 ratio, with `store-postgres` idle: the ordinary packages take about 4
  minutes, the three lead shards (about 37, 14 and 13 minutes) start first,
  two more workers join when the full Go suite ends near minute 32, and the
  queue drains at about minute 60 of a step that starts near minute 5 of the
  70-minute job and still owes lint about 3 minutes. That leaves no headroom.
  At a ratio of 1.6 or more, or when the batch also touches `internal/store`
  and `store-postgres` holds two cores for its 23 minutes, the race work
  (about 164 core-minutes for controller and acceptance at 1.31) exceeds what
  four cores can supply inside the job deadline under any schedule.

  Hosted CI therefore gives touched-package race tests a separate four-core
  runner and a full 70-minute execution window. All four shard workers are
  available from the start of that job. The full Go suite, conditional
  PostgreSQL suite and lint consume another runner's budget. The package
  selection, lead shards, ordinary packages, package timeouts and release-only
  store race coverage stay unchanged. At 1.31, the measured 164 core-minutes
  for controller and acceptance have up to 280 core-minutes available in the
  dedicated window, before ordinary packages, compilation and listing. This
  removes the shared-runner capacity shortfall. A complete hosted batch under
  the split is still unmeasured.

- **Fixture SQL connection reuse, 2026-11-01:** `teststore.Exec` retains one
  connection per registered store, closed by the opening test's cleanup. Its
  SQLite DSN keeps foreign keys, the busy timeout and immediate transactions.
  `SQL(t, st)` keeps its separate pool for corruption tests. The retained
  connection avoids reparsing the schema for every fixture statement.

  Normal samples compared `6065a30b9` with `f06454e8f` on the
  16-logical-CPU Linux host using Go 1.26.10, `GOMAXPROCS=4`, `GOWORK=off`,
  `GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`, private homes and a
  private temporary directory. The command was `go test -p 12 -json -count=1
  -timeout 120m ./internal/controller/... ./internal/store/...
  ./tests/acceptance/...`. An `-exec` wrapper recorded each test binary's
  start before executing it. The initial baseline binaries were compiled
  before changing the helper. The updated helper suite adds two regressions.
  Every other package ran the same top-level names.

  | Normal sample | Initial baseline | Clean baseline repeat | After |
  | --- | ---: | ---: | ---: |
  | Full command wall | 1342.270 s | 935.440 s | 944.970 s |
  | Full command CPU, user + system | 2257.300 s | 1941.110 s | 1878.300 s |
  | `internal/controller` wall | 1235.251 s | 871.644 s | 863.498 s |
  | `internal/store` wall | 1210.223 s | 927.453 s | 938.127 s |
  | `tests/acceptance` wall | 513.305 s | 336.539 s | 381.005 s |
  | `internal/store/teststore` wall | 2.660 s | 1.664 s | 1.496 s |

  The updated command passed. The normal baseline lacked web `tsx` and the
  TypeScript SDK's locked dependencies, so
  `TestDashboardRequestsReachControllerHandlers` and `TestTypeScriptBoundary`
  failed. Installing the locked dependencies made both focused reruns pass.
  The dashboard rerun used its original binary. The clean baseline repeat
  compiled the original Go sources through `-overlay` after dependency setup
  and passed the full command. Its wall time is within 1.1% of the updated
  sample, so these samples do not establish a gain in normal wall time. No
  existing assertions changed. Command wall includes compilation. Runs
  overlapped other samples and other lanes, with different cache warmth and
  variable host load, so these are
  observed shared-host costs.

  Race samples used the same command with `-race`. All packages except the
  unsharded `internal/store` completed successfully in both revisions.

  | Race sample | Before | After |
  | --- | ---: | ---: |
  | Full command wall, store timeout | 7388.810 s | 7235.910 s |
  | `internal/controller` wall | 6599.623 s | 4181.674 s |
  | `tests/acceptance` wall | 3518.984 s | 3072.251 s |
  | `internal/store/teststore` wall | 32.942 s | 11.366 s |
  | Unsharded `internal/store` wall, timeout | 7200.338 s | 7200.288 s |

  Both unsharded commands failed at the store's 120-minute timeout. The
  baseline had passed 753 top-level tests and the updated run 787, with no
  correctness failures reported before either timeout. These store samples
  establish lower bounds rather than completed suite costs.

  The full store race comparison therefore used four exhaustive sorted
  round-robin shards, each running a retained race-enabled test binary once
  with `GOMAXPROCS=1` and `-test.timeout=180m`. Both binaries list the same
  1,856 top-level Test, Example and Fuzz names. Their sorted LF-terminated
  list has SHA-256
  `b0c3f77c2c408c9e0c033c9894d7c56c3ada90df51a32e60fbd18f832678e65d`.
  Both runs include the slow class. Each name belongs to exactly one shard,
  and completion checks reject missing names or test failures. The four
  workers run concurrently within each revision, with the two revisions run
  sequentially. Compiling and listing the package binaries are outside the
  shard timing.

  | Four-shard store race sample | Before | After |
  | --- | ---: | ---: |
  | Suite wall | 3943.941 s | 3255.259 s |
  | Suite CPU, user + system | 13310.561 s | 11125.046 s |
  | Shard 0 wall | 3425.553 s | 2822.166 s |
  | Shard 1 wall | 3098.942 s | 2522.560 s |
  | Shard 2 wall | 3158.799 s | 2591.540 s |
  | Shard 3 wall | 3943.917 s | 3255.238 s |

  Both full-class store runs passed 1,797 top-level names and retained the
  same 59 conditional skips. All eight shards exited successfully, with no
  missing or failed names. Wall time fell about 17% and CPU about 16% in
  these shared-host samples.

- **Heavy packages:** run these by name rather than reaching for `./...`, and
  give the command a timeout the package actually fits in. Measured on a
  16-core Linux box, `GOWORK=off go test -count=1 <pkg>`. The CLI sample
  used Go 1.26.10 alongside other gates; the other samples ran alone:

  | Package | Measured | Suggested `-timeout` |
  | --- | --- | --- |
  | `./cmd/sparkwing` | 399s package / 409s command wall | 10m |
  | `./internal/orchestrator` | 121s | 10m |
  | `./internal/cluster` | 4s | 2m |

  The full CLI package passed all 1,130 top-level tests and fuzz targets in
  398.535s. Command wall time was 408.661s. The measurement used
  `go test -json -count=1 -timeout 90m ./cmd/sparkwing`, `GOWORK=off`,
  `GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`, private homes, and
  `TMPDIR=/var/tmp/sw-lead-tmp`. Before fixture reuse, the same class took
  775.644s (780.969s command wall). Pipeline build calls fell from 112 to 53;
  both runs built the CLI three times and passed the same test names without
  skips. These are ordinary runs, separate from the race-shard samples above.

  SDK fixtures share a package-owned source snapshot and binary cache because
  installer re-execs otherwise enter fresh test homes. Source changes,
  ignored embedded inputs, uncached debug builds, and offline compilation
  retain real compiler coverage. Independent subprocess tests run in parallel.
  Contention can still extend these samples; the suggested timeouts leave
  room above the measurements.

- **Normal broad check:** `sparkwing run gate` covers every committed Go
  module, the dashboard TypeScript unit, full ESLint, production build,
  and browser smoke suites, formatting, vet, build, tests, documentation
  mirrors, and source policy. It also runs `go test -race` on the packages
  whose Go files changed (staged, or since origin/main when nothing is
  staged), so a change never reaches main without the race detector having
  seen its own package, and runs `store-postgres` when that change touches
  `internal/store`. `internal/store` itself is the one exception to that race rule: it
  cannot finish inside the step's budget, so `pre-release` races it and the
  gate names the deferral in its log rather than skipping it silently. Unit and ESLint run in parallel; the production build then
  feeds the browser suite.
- **Gating beside other agents:** run independent checks concurrently in separate
  development worktrees when CPU and memory headroom allow. Use `sparkwing run`
  for declared pipelines so admission can account for their reservations;
  focused commands outside a run also consume host resources. Coordinate tests
  that share mutable services, same-repository landings, and production cutovers.
  There is no blanket single-gate lock. `sparkwing queue list` shows running and
  queued work; keep each pipeline's required checks and budgets in force.
- **Gating a branch beside a released daemon:** when the branch's pipeline
  binary carries a newer runs-store schema than the sparkwing hosting this
  machine's admission daemon, admission refuses the run. The refusal names both
  versions, `sparkwing daemon status` for the build the daemon runs, and the
  upgrade. A branch schema that no release carries yet is the case here, so
  install this checkout with `SKIP_WEB_BUILD=1 bash bin/install.sh` and run
  `sparkwing daemon restart`; `sparkwing update` is the answer only once the
  schema ships in a release. Either way the machine keeps one daemon and every
  run keeps its place in `sparkwing queue` and the dashboard.
- **Running against a home of your own:** `SPARKWING_HOME=DIR` points one
  command's state, cache and logs at DIR, which is isolation for work
  that must not touch the operational runs store, the release preview under
  Decisions before landing being the case that needs it. A run started that way
  is arbitrated by whatever daemon lives in that home rather than the machine's,
  so it is invisible to `sparkwing queue` and the dashboard and contends with
  every other run on the OS. It is not a way around a full queue. It does not
  move the user config directory, which answers to `XDG_CONFIG_HOME` and the
  per-file overrides, because what lives there is machine-wide and outlives any
  one home. Every config write from a command running under a home of its own
  is refused rather than sent to the machine's config, naming both paths and
  the value that keeps the write inside the home: `SPARKWING_CONFIG` for
  `config.yaml`, `SPARKWING_SECRETS_KEY_FILE` for the local secrets key. The
  local secrets themselves live in the home's own `state.db`, and the home
  does not import the machine's `secrets.env` or `config.env`. No variable
  moves `version-hold`.
- **Lint rules:** golangci-lint judges only code new since origin/main. Uploaded
  working trees may have no remote refs or parent history. In that case, the
  gate compares against an empty Git tree, judges every tracked file, and
  reports that wider scope. The same baseline drives scoped source checks
  and lint. The enabled linters also reject `_ = call()` on an error-returning call, nil
  returned after an error was observed, and work started on a context that is
  not the caller's. Drop an error only through a helper that logs why.
- **Dependency boundaries:** `.golangci.yml` keeps the depguard layering rules
  with the other linters, judging changes since `origin/main`. The fast push
  tier includes depguard over the packages the push touches.
- **No sleeps or wall-clock waits in tests:** `internal/sleepcheck` fails any
  `_test.go` that calls `time.Sleep`, `time.After`, `time.Tick`,
  `time.NewTimer` or `time.NewTicker`, or that reads `time.Now` or `time.Since`
  as a wait: an ordering comparison, a loop condition, or a
  `context.WithTimeout` or `WithDeadline` argument. A `time.Now()` that only
  stamps a fixture value is allowed. Write the test to wait on the channel,
  condition, or state the code under test signals, to drive a clock the test
  injects, or to run under `testing/synctest`, whose clock advances once every
  goroutine is blocked. A file that dot-imports `time` cannot be judged and
  fails for that reason. The `test-sleeps` step runs the checker in
  `pre-commit` and `gate` over the staged change, or the change since
  origin/main when nothing is staged, so a new offender fails from the first
  run while the tests written before the rule keep passing until they are
  edited. `GOWORK=off go run ./internal/sleepcheck .` judges every test file in
  the tree, which is how to size the remaining purge. The external-boundary
  allowlist covers the paced runner status polls in
  `internal/controller/direct_source_e2e_test.go` and
  `cmd/sparkwing/local_runner_restart_linux_test.go`, and the OS pipe-drain
  deadline in `internal/nodehost/host_exec_stdin_unix_test.go`. Runner cleanup
  uses a context hang bound and needs no exception.
  Each `time.After` has a `// sleepcheck:external-boundary` reason immediately
  above its select receive arm. The checker allows only the listed path,
  function, duration, select arm and exact marker, and fails if an exception
  disappears or changes or a marker has no approved wait. New waits and sleeps
  still fail.
- **Go directive policy:** the release cut and `pre-release`, beside its version
  checks, reject patch-level Go requirements in the published root module and
  any `go.mod` shipped in the template registry. `go 1.26` and `go 1.26.0`
  pass; `go 1.26.3` fails. Pipeline and fixture modules are excluded. Init and
  pipeline scaffolding share a generator that takes only the running Go
  version's major and minor; this step runs a focused scaffold test against a
  patch-version toolchain to check the generated module through pipeline creation.

- **Expensive or release-boundary:** `sparkwing run pre-release` adds race, chaos,
  vulnerability, dependency-freshness, API, and Terraform gates. Use
  `integration`, `template-verify`, `static-analysis`, and image builds only when
  the change touches those boundaries. `sparkwing run security-scan` runs gosec,
  source-mode govulncheck, gitleaks, and `npm audit`. The Security workflow runs
  it on every pull request and uploads gosec findings to code scanning. The
  release workflow runs neither it nor CodeQL against the tagged commit; the
  run that covered that commit on main is the scan of record. CodeQL reports
  alerts; gosec, govulncheck,
  gitleaks, and `pnpm audit` fail the gate. The advisory scanner retries a
  registry that times out or answers 5xx, and reuses a recorded pass for a day
  when `web/pnpm-lock.yaml` and `web/package.json` are byte-identical to the pass,
  so an unreachable registry fails as its own error rather than as an advisory
  and an unchanged dependency set is still re-asked daily. Run the local
  pipeline when a change touches an HTTP handler, auth, file paths built from
  input, subprocess arguments, or a dependency. Verify dashboard changes
  against real local state with `bash bin/dev-start.sh` (dashboard backend on :4343, `next dev` on :3100)
  and stop it with `bash bin/dev-stop.sh`; the browser gate uses deterministic
  API fixtures on OS-assigned local ports and does not replace that product
  exercise or exercise Kubernetes.
- **Template verification proofs:** `template-verify` scaffolds, builds, lints,
  and explains all registry templates, and runs the runnable ones. The build-cli
  job renders template text and verification manifests through the discovered
  local sparks-core templates module (SPARKWING_SPARKS_CORE_DIR, go.work, or a
  sibling checkout), or the pipeline module's published templates pin when no
  checkout is found. Its annotation names the selected source. The selected
  registry generates the verification jobs, so local additions and removals need
  no pipeline module replacement.
  A local
  run reuses a recorded proof for any template whose proof inputs are
  byte-identical to a previous pass. The digest covers, in one hash per
  template: the template's registry files (every file under its directory in
  the embedded `templates.FS`); the manifest's verification fields (tier,
  fixture, `verify_params`, `verify_tools`); the state of the sparkwing
  checkout, which is what supplies the SDK every scaffold replaces, the CLI
  each step invokes, and this verifier's own source; the same for the local
  sparks-core checkout; `go env GOVERSION GOOS GOARCH`; and the resolved path
  and `--version` output of every host tool the template's fixture and
  `verify_tools` need, plus, for Docker, whether the daemon actually answers
  `docker info`, because the binary being on PATH is not what the run step
  needs. A proof-format constant sits in the same hash, so widening or
  narrowing that list invalidates every recorded proof.

  A checkout's state means `git rev-parse HEAD`, `git diff --binary HEAD`, and
  the contents of every non-ignored untracked file, plus two gitignored inputs
  that change what gets verified and would otherwise be invisible: `go.work`
  (which steers the plain `go build` that produces the verify CLI, and the
  sparks-core discovery that pins a scaffold's modules) and
  `internal/web/next-out` (embedded into that CLI). Any other gitignored file
  that reaches a build is outside the digest; add it to that list when one
  appears.

  Reuse is fail-closed. Any input that cannot be established refuses reuse and
  verifies the template again: a checkout that is not a git repository, an
  unreadable untracked file or build input, a template that will not digest,
  and in particular the absence of a local sparks-core checkout, because
  without one a scaffold's `go mod tidy` resolves published module versions
  that no digest covers. A partial verification is never recorded: when a
  runnable template's toolchain is missing the run step is skipped, the gate
  stays green, and no proof is written, so the template is verified again once
  the toolchain appears. Dry runs read the admitted run's job and step status.
  Any skipped execution is partial verification, including a body skipped as
  `no_dry_run_defined`. It reports no completed run step and records no proof.
  Missing or unreadable execution evidence fails verification.
  Reuse never changes the generated fan-out: every selected template keeps its
  node, and the decision is made inside it, so a digest miss cannot be lost in
  plan shaping. Third-party module resolution is outside the digest either way,
  which is the standing reason the boundary stays exhaustive: the release
  pipeline passes `--exhaustive`, and so should any manual run being used as a
  release proof.

  Recorded proofs live in `template-verify-proofs/` under the sparkwing
  directory of the OS user cache directory (`~/Library/Caches/sparkwing` on
  macOS, `${XDG_CACHE_HOME:-~/.cache}/sparkwing` on Linux), one JSON file per
  digest naming the template, its tier, and whether the run step executed. A
  file that does not parse or carries another proof format is ignored, writes
  are atomic, and recording prunes anything older than 14 days. Delete the
  directory to force full verification without `--exhaustive`.

- **Store dialect matrix:** `sparkwing run store-postgres` runs the
  `internal/store` tests that exercise PostgreSQL with
  `SPARKWING_TEST_STORE=postgres`, so every store test that opens through
  `internal/store/storetest` exercises the Postgres dialect instead of a SQLite
  file. It compiles one `internal/store` test binary, lists its top-level
  Test, Example and Fuzz names, and runs every one except two kinds. The
  internal tests in the files named by
  `.sparkwing/jobs/store_postgres_sqlite_only.txt` are skipped: the internal
  package cannot import `storetest`, so those tests and fuzz seeds only ever
  open SQLite, the full Go suite already runs them, and they cost 432 s of a
  24m32s hosted-profile run. `TestClaimSecurityRegressions` follows the suite's
  dialect and so repeats `TestPostgresClaimSecurityRegressions` here; only the
  PostgreSQL copy runs. Any other test runs, including every test in a new
  file, so a PostgreSQL path the source scan cannot see, such as a non-test
  helper, is still exercised. The step logs the count and SHA-256 of the
  listed, selected and skipped names, and fails when a listed name has no
  declaration or is placed other than exactly once, or the duplicate pair is
  no longer listed. It also fails, as does
  `TestStorePostgresSelectionCoversTheStoreInventory` in `.sparkwing/jobs`,
  when a named file is missing, declares no internal test, or reaches
  `SPARKWING_TEST_PG_URL` or the `pgx` driver directly or through a helper or
  constant in the internal test files; remove such a file from the list. The
  sorted selection is assigned round-robin, each
  name exactly once, to one `GOMAXPROCS=1` shard process per core a Go step
  may hold: four on the hosted four-core runner, the Go step bound
  (`(cpus-1)/2` shared, `cpus/2` under `CI`) elsewhere. Each shard gets a
  database of its own, created on the server before the shards start and
  dropped `WITH (FORCE)` afterwards, because the store's migration advisory
  lock is per database: shards sharing one database queue every fixture's
  schema apply behind each other. The shard URL drops `dbname` and `database`
  query options, which pgx reads over the URL path, and a `service` reference
  is refused. A run killed outright can leave `sparkwing_store_shard_*`
  databases on a configured server, and so can a `CREATE DATABASE` whose
  success reply is lost: the server committed it, but the job saw an error and
  never tracked the name. Both are rare and fail the run; drop the leftovers
  by hand with `DROP DATABASE <name> WITH (FORCE)` once no run is using them. A failing shard
  stops the others. `internal/store/internal/storetest` and
  `internal/store/teststore` run whole. Measured on 2026-11-02 on a loaded
  16-core host pinned to four cores with `CI=1` against a shared PostgreSQL 18
  server: the former single `go test ./internal/store/...` took 28m12s for
  1,876 top-level tests; selection alone took 14m26s; selection with four shards
  took 7m10s, and a verbose repeat 6m07s whose shards took 3m10s to 6m04s with
  each of the 508 selected names passing exactly once. After the selection
  moved to a reviewed file list, 514 names run: the five other tests in
  `claim_requirements_test.go` and `TestStoreSQLTrustLiterals` share a file
  with a PostgreSQL test. A verbose four-shard run on the same pinned cores,
  with the host otherwise idle, took 5m37s, shards 3m15s to 5m34s, each of the
  514 passing exactly once. Round-robin leaves
  `TestAdmissionDeterministicSimulation` (about two minutes) in the longest
  shard. It reuses `SPARKWING_TEST_PG_URL` when
  that is set and otherwise starts an embedded Postgres on a free port (data
  under a temporary root, binaries cached in `$TMPDIR`), then stops it and
  removes the data directory whether the suite passes, fails, or is
  interrupted. A run killed outright before its teardown finishes, as a
  cancelled run is one second after it is asked to stop, leaves a
  `sparkwing-store-postgres-*` directory under `TMPDIR`; the next run removes
  every such directory whose owning process is gone. No Docker. A failing suite prints the tail of the server log,
  which embedded-postgres only makes available once the server has stopped.
  A server that will not start is retried once on a fresh port and then
  fails the step. `pre-release` runs it after the race gate, under a
  thirty-minute timeout. Each shard has a 25-minute Go timeout, so a hung test
  reports its own stack only when the work before the shards start (embedded
  server start or download, the helper packages, compiling, listing and
  creating the databases) finishes within the step's other five minutes; past
  that, the step's thirty-minute deadline cancels the shards without a stack. The first run may also download the Postgres binaries.

- **Hosted pre-release PostgreSQL:** the canonical pre-release job supplies a
  PostgreSQL 17 service through `SPARKWING_TEST_PG_URL`, so `store-postgres`
  uses that server without downloading embedded binaries. The separate
  Postgres conformance job verifies the backup drill with a matching client
  and rejects skipped tests. Its service shares `/var/run/postgresql` with the
  runner so the Unix socket identity test exercises a real socket connection.
  Its no-skip selection also includes
  `TestCLITokenMintUsesCommittedMembershipAuthority` in `internal/controller`,
  retaining the PostgreSQL bearer-token regression for demotion and removal.
  It also retains both SQL literal regressions, which verify that run filters
  and secret mutations treat SQL metacharacters as data and preserve team
  isolation. `sparkwing run integration` runs them through its full `./...`
  suite against the local PostgreSQL fixture.

- **Native Windows CLI smoke:** `TestWindowsLocalRunStartsControllerAndFinishes`
  (`cmd/sparkwing`) builds the CLI and runs a Go pipeline through standalone
  and shared controllers in private homes. Both modes require a successful
  `run_finish` receipt and a zero process exit status. Run it on Windows with
  `go test -p 1 ./cmd/sparkwing -run TestWindowsLocalRunStartsControllerAndFinishes`.
  `TestWindowsLocalHTTPKeepsRequestAliveAcrossReuse` in `internal/wingd`
  guards the named-pipe deadline contract beneath startup and runner claims.

- **Standalone client shutdown:**
  `TestStandaloneRunClosesUnusedClientDialBeforeControllerDrain` (`cmd/sparkwing`,
  Unix) cancels a request while its dial completes, then shuts down the real
  standalone runtime. It requires the CLI to close the unused connection before
  controller draining, without changing the API drain deadline. Native Windows
  verification repeats standalone pipeline runs and requires both successful
  `run_finish` receipts and zero process exits.

- **Local controller smoke:** `TestLocalControllerSmoke` (`cmd/sparkwing`,
  part of the full suite) builds the CLI and drives every local controller
  command against a fresh local controller in a private home. It fails when
  the host refuses unix sockets; a sandbox that is known to refuse them sets
  `SPARKWING_TEST_NO_LOCAL_SOCKETS=1` to skip it instead.

- **Postgres conformance:** the store, backend, and orchestrator Postgres
  suites skip when `SPARKWING_TEST_PG_URL` is unset, and fail when it is
  set to a database they cannot reach. Start one with `docker run --rm -d
  --name sw-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:17`,
  then run `SPARKWING_REQUIRE_PG=1
  SPARKWING_TEST_PG_URL=postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable
  go test -v -run 'Postgres|Pg' ./internal/store ./internal/backend
  ./internal/orchestrator`. `SPARKWING_REQUIRE_PG` turns an unset URL and
  `SPARKWING_INTEGRATION_DISABLE=1` into failures. That is not proof on
  its own, so both lanes that promise Postgres coverage run those suites
  verbosely and fail on any `--- SKIP` line: the hosted `Postgres
  conformance` job in `.github/workflows/ci.yaml` on its nightly and manual
  dispatch tier, and `sparkwing run integration` against its
  Dockerized Postgres and MinIO. The hosted job runs the three packages
  under `-short`, leaving the names its no-skip step selects to that step, because the canonical gate already runs them in full on
  SQLite and the full store suite overran Go's ten-minute package limit on
  a hosted runner; the tests that switch to the service database stay in
  the short class.
- **Postgres client on the hosted lane:** the `Postgres conformance` job
  installs `postgresql-client-17` from the PostgreSQL Apt repository and
  points `SPARKWING_PG_BIN` at `/usr/lib/postgresql/17/bin`, because
  Ubuntu's own repository tops out at 16 and a `pg_dump` older than its
  server refuses the dump outright. `SPARKWING_PG_BIN` names the real
  binaries rather than `/usr/bin/pg_dump`, which is postgresql-common's
  wrapper and picks a version from the clusters the image happens to
  have. A step prints both client versions and fails when the major is
  below 17, so a repository that stops shipping 17 fails the job instead
  of quietly sending the tests back to skipping. `sparkwing run
  integration` installs nothing and takes whatever client the developer's
  machine has.
- **Backup and restore drill:** `TestBackupRestoreDrill` in `internal/store`
  checks durable state through a backup and restore. Its SQLite
  subtests always run. Its Postgres subtest shells out to `pg_dump` and
  `pg_restore`, taking them from `SPARKWING_PG_BIN` or `PATH` and
  skipping when neither supplies them. Run it against a server with
  `SPARKWING_TEST_PG_URL=... SPARKWING_PG_BIN=/usr/lib/postgresql/17/bin
  go test -run TestBackupRestoreDrill -v ./internal/store`. The drill's name
  carries no dialect, so the hosted no-skip step selects it by name:
  `-run 'Postgres|Pg|BackupRestoreDrill'`. That is what turns a missing
  or too-old client into a red lane rather than a green one with a
  skipped subtest. `sparkwing run integration` still selects
  `Postgres|Pg` only, so the drill can skip there unnoticed.
- **Kubernetes product path:** `sparkwing run k8s-e2e` proves authenticated
  uploaded-source intake, runner execution, logs, cancellation, retry, restarts, and
  retained state against an explicit Kubernetes context and caller-supplied
  image set. It requires a signed multi-team license through
  `SPARKWING_K8S_E2E_LICENSE_FILE` and an exact namespace/release cleanup
  allow-list. It never creates or deletes cluster infrastructure. GitHub webhook
  intake and remote Git fetching need separate evidence. This check is manual and
  opt-in because it uses a real cluster. The fixture's SDK require pin follows
  the release pins, with a local replace to the checkout. The harness bundles
  the committed SDK source from HEAD and rewrites that replace inside the
  uploaded repository so runner compilation needs no caller checkout.

The hosted canonical gate installs `sdk/typescript` dependencies with
`npm ci --ignore-scripts` before Go tests, because the native protocol and
conformance tests compile the TypeScript SDK. Local runs need the same install.

## Decisions before landing

- **Review:** use an independent reviewer for public SDK/CLI contracts, cache or
  admission invariants, concurrency, process lifecycle, persistent schemas,
  security boundaries, or release machinery. Otherwise state why it was not
  valuable.
- **Documentation:** keep README, CLI help, source docs, examples, generated
  references, and `pkg/docs/mirror/` aligned with behavior. Run the owning drift
  check instead of editing generated mirrors by hand.
- **Generated files:** `bash bin/regen-all.sh` rewrites every derived file
  (cli/config/sdk/api references, `api/openapi.yaml`, `.apidiff/`,
  `pkg/docs/mirror/`, and the vendored runner-bundle tarball) in one pass and
  is a no-op on a clean tree. Resolve a merge conflict in any of them by
  taking either side, running that script, and committing the result.
- **Changelog:** notable adopter-facing behavior belongs in a unique
  `docs/changes/<slug>.md` with a `###` category and scope-prefixed bullets.
  Breaking guides go in `docs/migrations/unreleased/<slug>.md`, starting with
  an H2. Follow `CHANGELOG-STYLE.md` and run `bash bin/sync-docs.sh` to mirror
  the fragments. Keep shared unreleased aggregates empty. Embedded docs,
  weekly-release and release gates collect fragments; the release cut writes
  the versioned notes, repoints guide links and deletes consumed fragment pairs.
- **Tests:** record the focused checks selected, or why execution was waived.
  Do not run every race, Docker, or integration suite by default.
- **Release:** merging is not a release; a release is a tag push. Select an
  exact source with `sparkwing run release -- --commit SHA` (default `HEAD`).
  The source must be an ancestor of freshly fetched `origin/main`.
  The release owns a detached worktree and compiles `release-cut` from
  the selected source, using a private `SPARKWING_HOME`. The source must
  contain the `release-cut` entrypoint. The invoking checkout stays untouched,
  including uncommitted work. Lands continue on main throughout the cut.
  Verification follows the manual fallback and weekly CI policy described
  above. Incremental push CI is not full-suite evidence. Published module
  freshness, Go directive policy, coherent SDK
  pins, changelog links and docs sidebar checks still run. The cut prepares
  notes, migrations and SDK pins, then checks schema/wire coverage before
  committing those paths with `--no-verify`.
  The annotated tag points to that prepared release commit, whose source
  parent remains the selected SHA. A separate detached worktree merges that
  commit into freshly fetched main. It removes only unchanged selected entries
  from main's `[Unreleased]` section, keeps newer or edited notes there, and
  copies the release section unchanged. The merge preserves main's newer code.
  Publication atomically pushes the merge to main and the tag to origin. If
  main moves during the push, only the metadata merge and push are retried,
  with the same release commit and tag. A conflict or repeated contention
  stops publication for manual recovery; it does not require freezing main.
  Failed cuts retain their worktrees at the paths printed in the logs.
  Preview with
  `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run -- --commit SHA`,
  then use `--sw-allow-risk destructive,prod` for publication. Within one controller, manual and weekly
  releases share a release concurrency group. Release owners serialize cuts
  across isolated homes; lands use their own path.
  From the tag push on, `.github/workflows/release.yaml` resolves the tag,
  builds, signs and publishes artifacts, and creates the GitHub release.
  Notes come from the tag's changelog section, with the annotated tag message
  and a pointer to CHANGELOG.md as fallbacks. Handle failures through the
  [release failure policy](#release-failure-policy).
- **Independent verification:** for user-facing local-execution changes, build
  the intended revision with `SKIP_WEB_BUILD=1 bash bin/install.sh` when the web
  bundle is unchanged, then exercise the installed CLI and daemon. To exercise a
  branch against real runs without replacing the binary every other repo and
  timer resolve, install it under its own name beside the real one:
  `SPARKWING_INSTALL_NAME=sparkwing-crons bash bin/install.sh` writes
  `~/.local/bin/sparkwing-crons`, which shares the home and store, so it must
  be built at the same store schema as the real one. Verify SDK,
  templates, integrations, browser behavior, or release assets when those
  surfaces changed.

## Weekly SDK and CLI delivery

`weekly-release` runs Monday at 09:00 America/Denver. It selects freshly
fetched `origin/main` by default, or `--commit SHA`, and uses the same detached
worktree cut and exact-source CI check as `release`. It skips when the selected
source has no `[Unreleased]` entries. Its default minor bump stays on v0.x.
Release cuts are pre-approved. The scheduled entrypoint supplies the cut's
`--sw-allow-risk destructive,prod` authorization. Main keeps accepting lands
while the cut runs. Publication merges the prepared metadata into fresh main
and atomically pushes that merge with the tag on the separate release commit.

That tag makes the Go SDK module available and starts the existing
`.github/workflows/release.yaml` publication steps for signed CLI binaries,
images and the TypeScript package. Before cutting, the weekly pipeline starts
or resumes the Bitwing `sparkwing-release` workflow using the version as its
idempotency key. It records the cut's immutable tag and commit in a blocked
`cut` handoff. The release owner resumes with `bitwing workflow next --run WR-N`
to confirm hosted publication, publish matching sparkwing-product docs, verify
production through kikd-infra, and reconcile downstream pins. The checklist
stays open until every required step has evidence. A failed cut retains the
detached worktree at the path printed in its job log.

Run the same path sooner, or preview the cut:

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release -- --bump patch
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release -- --version v0.70.0
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release --sw-dry-run
```

`owned-sdk-main` runs daily at 08:00 America/Denver. It resolves green
Sparkwing main to an exact v0 Go module version. For each eligible repository,
it sets both the SDK requirement and a versioned replacement to that version,
tidies the pipeline module, commits only its go.mod and go.sum, and invokes
`xwing env land`. The replacement lets development pipelines use the installed
host without requesting an unpublished signed engine release.

Eligible repos are immediate children of `~/code` with a Go pipeline module,
a GitHub origin owned by `koreyGambill` or `sparkwing-dev`, and an Xwing policy
with `landing.branch: main` and a verification command. Pull-request policies
and Sparkwing itself are excluded. Xwing applies the repo's committed landing
policy and verification. An unchanged main pin is skipped. A retained candidate
that already has the target pin still retries landing and its verification. A failed update retains
its Xwing environment for repair, while the pipeline tries the remaining repos
and reports all failures. TypeScript repos keep their published SDK pins and
can use `sparkwing pipeline upgrade` after a release.

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run owned-sdk-main --sw-dry-run -- --repos-root "$HOME/code"
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run owned-sdk-main -- --repos-root "$HOME/code"
```

`desktop-update` runs daily at 07:00 America/Denver. It hands activation to
`sparkwing-desktop-update.service` because the scheduling run must finish before
its controller can restart. The user unit clones origin/main, requires green
push CI at that SHA, and builds the CLI, runner and dashboard in a private
directory beside the installed binaries. It then waits up to 30 minutes for
runs, queued triggers and held leases in both the default and scheduling state
homes, plus active systemd land units, to finish. A failed inspection or timeout
fails the unit and leaves the installed binaries unchanged. Once idle, it
renames the staged binaries into `~/.local/bin` and restarts the controllers
that were running. The standalone runner service keeps its running process.
Its replacement binary takes effect at that service's next restart.
Builds and restart commands stay in the updater's cgroup and stop with its unit.
Replacement controllers start in separate scopes so they survive updater exit.
Before replacing binaries, the worker reads each running controller's key and
config selectors from its process environment. It restores those selectors only
for that controller's restart, keeping the default and delivery stores on their
existing keys. An unreadable environment refuses the update before either swap.
If the CLI rename fails after replacing the runner, the updater restores the
previous runner and exits with a retry message. A failed restore names the
retained binary and destination for manual repair. Replayable keyed run intake
can wait up to 30 seconds across a controller restart, covering land starts
that arrive after the idle snapshot.
Install the intake retry fix while the queue is idle before arming unattended
updates. Processes already running an older CLI keep that client's retry budget.

The pipeline result confirms the handoff. Read the activation result with
`journalctl --user -u sparkwing-desktop-update`. Use
`systemctl --user status sparkwing-desktop-update` while it runs. Concurrent updater attempts
refuse rather than replacing an active updater. Failed attempts retain their
staging directory, printed in the journal, for inspection. The updater never
cancels work or stops land units. Continuous activity defers the update to a
later invocation. New work can still arrive while it waits.

Preview the handoff without building, installing or restarting anything:

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run desktop-update --sw-dry-run
```

Arm the declarations from the main checkout on a host with Git push access,
GitHub CLI authentication for CI reads, Go, Bitwing, Xwing and Sparkwing on PATH:

```sh
export SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery"
export SPARKWING_SECRETS_KEY_FILE="$SPARKWING_HOME/secrets.key"
sparkwing schedules install -C "$HOME/code/sparkwing" --schedule weekly-release/weekly-sdk-cli,owned-sdk-main/owned-sdk-main,desktop-update/desktop-main --unpinned
sparkwing schedules list
```

The dedicated state directory keeps release work out of the operational runs
store. The private key-file provider also works on headless hosts. Use those
same exports for manual runs. Keep HOME at the operator's home so Git signing and GitHub authentication
remain available. Sparkwing owns cadence and overlap handling through its
schedule evaluator and managed timer. `--unpinned` compiles the main checkout
on each fire so later pipeline fixes take effect. This installation command
selects only these schedules and preserves other installed schedules. The desktop
updater also needs a systemd user manager, `jq`, `flock`, `timeout`, pnpm and Go.

## Release failure policy

Main may be red. If a change breaks it and the fix is not obvious, revert that
change first. Fix local preflight refusals on main, rerun the affected checks
after the correction, and retry the cut. Fix a failed hosted build forward on
main and publish a later patch tag. Never re-cut or move a published release tag.

Do not rerun an unchanged failing check merely to obtain green. A demonstrated
environment or tool setup correction can justify rerunning the same source.
Retain the original failure and correction as evidence.
