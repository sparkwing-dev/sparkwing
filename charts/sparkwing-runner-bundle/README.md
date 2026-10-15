# sparkwing-runner-bundle

This chart deploys Sparkwing runners in a customer-owned namespace. It connects
to an existing controller. The controller and dashboard are included in the
`sparkwing-full` parent chart.

The chart has no cache server, git mirror or package proxy. Runners acquire source
from the repository and use the controller's signed data paths. Configure a
standard package mirror through `runner.extraEnv` if your deployment needs one.

Pin the runner image repository and tag to a compatible build. The chart's
`appVersion` fallback is metadata, not a tested public image set.

```sh
helm template sparkwing ./charts/sparkwing-runner-bundle \
  --set controller.url=https://controller.example.com \
  --set controller.tokenSecret.name=sparkwing-token \
  --set 'runner.repos[0]=github.com/your-org/*' \
  --set runner.image.repository=registry.example.com/sparkwing-runner \
  --set runner.image.tag=YOUR_BUILD
```

Create the controller credential Secret separately. The chart mounts it into the
runner's credentials directory. Pipeline code on that runner can read its runner
credential. Limit the credential to the runner's authorized work.

Set `runner.repos` to the repository patterns this machine may fetch, such as
`github.com/your-org/*`. The runner checks this list before hosting each award.
The default empty list admits no repositories. The full chart exposes it as
`sparkwing-runner-bundle.runner.repos`.

Go build and module caches use scratch storage by default. To preserve them across
pod replacements, enable `runner.goCache.persistence`. More than one runner replica
requires a `ReadWriteMany` volume. An existing claim can be selected through
`runner.goCache.persistence.existingClaim`.

The runner has its own ServiceAccount. Its token is not mounted by default
because the runner claims work through the controller and needs no Kubernetes
API access. The chart creates no runner Role or RoleBinding. The launcher owns
Kubernetes Jobs under its separate permissions. Set `runner.alsoClaimTriggers`
to `false` for a pool that should claim only jobs.

For a separately configured Cloud launcher, set `launcher.serviceAccountName`
to provision its Kubernetes identity, permissions, and admission exemption guard.
The cluster must serve the GA `admissionregistration.k8s.io/v1` validating
admission policy and binding APIs, and mutating admission policy binding API.
The launcher reads its namespace labels, mutating webhook configurations,
mutating admission policy bindings, and namespace LimitRanges before publishing
work. It also verifies the exact validating policy and binding installed by the
chart. Namespace and validating guard reads are restricted to their expected
resource names.

The launcher needs LimitRange `list` and `create` permission to probe guard
enforcement. It submits a valid LimitRange CREATE with Kubernetes dry-run enabled
and requires a Forbidden response naming the exact validating guard. The probe
persists nothing. The installed guard denies actual LimitRange creation in the
Cloud Job namespace as well. A missing or inactive guard prevents publication.

Use a dedicated Cloud Job namespace with no LimitRanges. Every mutating webhook
that could change Job or Pod CREATE/UPDATE, and every mutating admission policy
binding, must explicitly exclude the namespace using a namespace selector
`matchExpressions` entry with key `kubernetes.io/metadata.name`, operator `NotIn`,
and the release namespace in `values`. Object selectors do not prove exemption.
Configure those exclusions before launching.

The chart installs `sparkwing-cloud-exemption-<namespace>` as a
ValidatingAdmissionPolicy and binding with denial on failure. This guard rejects
future LimitRanges in the namespace, changes to its canonical namespace-name
label, and webhook or mutating policy binding changes that remove the required
exclusion. Cluster administrators must preserve the validating guard while Jobs
are live. The launcher refuses publication if the required policy API is
unavailable or the installed guard differs from its expected policy.

`values.yaml` lists the supported chart values. Cache-service values, credentials,
Deployments, Services, PVCs and NetworkPolicies have been removed.
