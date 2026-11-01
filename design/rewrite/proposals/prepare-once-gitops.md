'# Same-run preparation rollout proposal

Proposal only. No infrastructure or GitOps repository was changed.

kikd-infra needs no manifest change for this lane. The controller's existing
team-scoped data store holds the artifact. Binding and warm capacity changes
belong to their separate proposals.

kikd-gitops must update controller, launcher and job-runner images together and
re-render each environment with the chart from this change. The release owner
supplies immutable image digests after publishing; this proposal cannot name
an image that has not been built. Preserve storage, RBAC and network policies.
Remove fetch-source init containers and source-dir flags from custom Job templates.

The exact Kustomize overlays below target the policies in each environment's
checked-in rendered.yaml. They replace only validations affected by preparation;
image pins and binding variables stay unchanged. Merge with the binding lane's
policy changes at rollout. Drain active awards before changing launcher and
policy together: a mismatched policy refuses Job/Pod creation.

## sparkwing-staging

Place beside that environment's rendered.yaml for review/rendering.

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- external-secrets.yaml
- rendered.yaml
- checkout.yaml
patches:
- target:
    group: admissionregistration.k8s.io
    version: v1
    kind: ValidatingAdmissionPolicy
    name: sparkwing-cloud-pods-sparkwing-staging-cloud-jobs
  patch: "[\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/10/message\",\n    \"value\": \"Pods\
    \ run only the launcher's container set and pinned images\"\n  },\n  {\n    \"op\": \"replace\",\n    \"path\"\
    : \"/spec/validations/10\",\n    \"value\": {\n      \"expression\": \"!variables.create || (variables.shape\
    \ == 'job' ? size(variables.spec.containers) == 1 && variables.spec.containers[0].name == 'run-node' && variables.spec.containers[0].image\
    \ == variables.runnerImage && size(variables.inits) == 1 && variables.inits[0].name == 'docker' && variables.inits[0].image\
    \ == variables.dockerImage : size(variables.spec.containers) == 1 && variables.spec.containers[0].image == variables.runnerImage\
    \ && size(variables.inits) == 0)\",\n      \"message\": \"Pods run only the launcher's container set and pinned\
    \ images\"\n    }\n  },\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/15/message\",\n    \"\
    value\": \"the workload containers run only the launcher's sparkwing-runner commands\"\n  },\n  {\n    \"op\"\
    : \"replace\",\n    \"path\": \"/spec/validations/15\",\n    \"value\": {\n      \"expression\": \"!variables.create\
    \ || variables.shape != 'job' || variables.spec.containers[0].?command.orValue([]) == [\\\"sparkwing-runner\\\
    \"] && !has(variables.spec.containers[0].restartPolicy) && (size(variables.spec.containers[0].?args.orValue([]))\
    \ == 11 || size(variables.spec.containers[0].?args.orValue([])) == 13 && variables.spec.containers[0].args[11]\
    \ == '--job') && variables.spec.containers[0].args[0] == \\\"run-node\\\" && variables.spec.containers[0].args[1]\
    \ == \\\"--controller\\\" && variables.spec.containers[0].args[3] == \\\"--run\\\" && variables.spec.containers[0].args[5]\
    \ == \\\"--generation\\\" && variables.spec.containers[0].args[7] == \\\"--module-cache\\\" && variables.spec.containers[0].args[8]\
    \ == \\\"/tmp/modules\\\" && variables.spec.containers[0].args[9] == \\\"--bootstrap-file\\\" && variables.spec.containers[0].args[10]\
    \ == \\\"/etc/sparkwing/bootstrap/bootstrap\\\"\",\n      \"message\": \"the workload containers run only the\
    \ launcher's sparkwing-runner commands\"\n    }\n  }\n]"
- target:
    group: admissionregistration.k8s.io
    version: v1
    kind: ValidatingAdmissionPolicy
    name: sparkwing-cloud-jobs-sparkwing-staging-cloud-jobs
  patch: "[\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/8/message\",\n    \"value\": \"Pods run\
    \ only the launcher's container set and pinned images\"\n  },\n  {\n    \"op\": \"replace\",\n    \"path\":\
    \ \"/spec/validations/8\",\n    \"value\": {\n      \"expression\": \"!variables.create || (variables.shape\
    \ == 'job' ? size(variables.spec.containers) == 1 && variables.spec.containers[0].name == 'run-node' && variables.spec.containers[0].image\
    \ == variables.runnerImage && size(variables.inits) == 1 && variables.inits[0].name == 'docker' && variables.inits[0].image\
    \ == variables.dockerImage : size(variables.spec.containers) == 1 && variables.spec.containers[0].image == variables.runnerImage\
    \ && size(variables.inits) == 0)\",\n      \"message\": \"Pods run only the launcher's container set and pinned\
    \ images\"\n    }\n  },\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/13/message\",\n    \"\
    value\": \"the workload containers run only the launcher's sparkwing-runner commands\"\n  },\n  {\n    \"op\"\
    : \"replace\",\n    \"path\": \"/spec/validations/13\",\n    \"value\": {\n      \"expression\": \"!variables.create\
    \ || variables.shape != 'job' || variables.spec.containers[0].?command.orValue([]) == [\\\"sparkwing-runner\\\
    \"] && !has(variables.spec.containers[0].restartPolicy) && (size(variables.spec.containers[0].?args.orValue([]))\
    \ == 11 || size(variables.spec.containers[0].?args.orValue([])) == 13 && variables.spec.containers[0].args[11]\
    \ == '--job') && variables.spec.containers[0].args[0] == \\\"run-node\\\" && variables.spec.containers[0].args[1]\
    \ == \\\"--controller\\\" && variables.spec.containers[0].args[3] == \\\"--run\\\" && variables.spec.containers[0].args[5]\
    \ == \\\"--generation\\\" && variables.spec.containers[0].args[7] == \\\"--module-cache\\\" && variables.spec.containers[0].args[8]\
    \ == \\\"/tmp/modules\\\" && variables.spec.containers[0].args[9] == \\\"--bootstrap-file\\\" && variables.spec.containers[0].args[10]\
    \ == \\\"/etc/sparkwing/bootstrap/bootstrap\\\"\",\n      \"message\": \"the workload containers run only the\
    \ launcher's sparkwing-runner commands\"\n    }\n  }\n]"
```

## sparkwing

Place beside that environment's rendered.yaml for review/rendering.

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- external-secrets.yaml
- rendered.yaml
- stripe-webhook.yaml
patches:
- target:
    group: admissionregistration.k8s.io
    version: v1
    kind: ValidatingAdmissionPolicy
    name: sparkwing-cloud-pods-sparkwing-cloud-jobs
  patch: "[\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/10/message\",\n    \"value\": \"Pods\
    \ run only the launcher's container set and pinned images\"\n  },\n  {\n    \"op\": \"replace\",\n    \"path\"\
    : \"/spec/validations/10\",\n    \"value\": {\n      \"expression\": \"!variables.create || (variables.shape\
    \ == 'job' ? size(variables.spec.containers) == 1 && variables.spec.containers[0].name == 'run-node' && variables.spec.containers[0].image\
    \ == variables.runnerImage && size(variables.inits) == 1 && variables.inits[0].name == 'docker' && variables.inits[0].image\
    \ == variables.dockerImage : size(variables.spec.containers) == 1 && variables.spec.containers[0].image == variables.runnerImage\
    \ && size(variables.inits) == 0)\",\n      \"message\": \"Pods run only the launcher's container set and pinned\
    \ images\"\n    }\n  },\n  {\n    \"op\": \"add\",\n    \"path\": \"/spec/validations/-\",\n    \"value\": {\n\
    \      \"expression\": \"!variables.create || variables.shape != 'job' || variables.spec.containers[0].?command.orValue([])\
    \ == [\\\"sparkwing-runner\\\"] && !has(variables.spec.containers[0].restartPolicy) && (size(variables.spec.containers[0].?args.orValue([]))\
    \ == 11 || size(variables.spec.containers[0].?args.orValue([])) == 13 && variables.spec.containers[0].args[11]\
    \ == '--job') && variables.spec.containers[0].args[0] == \\\"run-node\\\" && variables.spec.containers[0].args[1]\
    \ == \\\"--controller\\\" && variables.spec.containers[0].args[3] == \\\"--run\\\" && variables.spec.containers[0].args[5]\
    \ == \\\"--generation\\\" && variables.spec.containers[0].args[7] == \\\"--module-cache\\\" && variables.spec.containers[0].args[8]\
    \ == \\\"/tmp/modules\\\" && variables.spec.containers[0].args[9] == \\\"--bootstrap-file\\\" && variables.spec.containers[0].args[10]\
    \ == \\\"/etc/sparkwing/bootstrap/bootstrap\\\"\",\n      \"message\": \"the workload containers run only the\
    \ launcher's sparkwing-runner commands\"\n    }\n  }\n]"
- target:
    group: admissionregistration.k8s.io
    version: v1
    kind: ValidatingAdmissionPolicy
    name: sparkwing-cloud-jobs-sparkwing-cloud-jobs
  patch: "[\n  {\n    \"op\": \"test\",\n    \"path\": \"/spec/validations/8/message\",\n    \"value\": \"Pods run\
    \ only the launcher's container set and pinned images\"\n  },\n  {\n    \"op\": \"replace\",\n    \"path\":\
    \ \"/spec/validations/8\",\n    \"value\": {\n      \"expression\": \"!variables.create || (variables.shape\
    \ == 'job' ? size(variables.spec.containers) == 1 && variables.spec.containers[0].name == 'run-node' && variables.spec.containers[0].image\
    \ == variables.runnerImage && size(variables.inits) == 1 && variables.inits[0].name == 'docker' && variables.inits[0].image\
    \ == variables.dockerImage : size(variables.spec.containers) == 1 && variables.spec.containers[0].image == variables.runnerImage\
    \ && size(variables.inits) == 0)\",\n      \"message\": \"Pods run only the launcher's container set and pinned\
    \ images\"\n    }\n  },\n  {\n    \"op\": \"add\",\n    \"path\": \"/spec/validations/-\",\n    \"value\": {\n\
    \      \"expression\": \"!variables.create || variables.shape != 'job' || variables.spec.containers[0].?command.orValue([])\
    \ == [\\\"sparkwing-runner\\\"] && !has(variables.spec.containers[0].restartPolicy) && (size(variables.spec.containers[0].?args.orValue([]))\
    \ == 11 || size(variables.spec.containers[0].?args.orValue([])) == 13 && variables.spec.containers[0].args[11]\
    \ == '--job') && variables.spec.containers[0].args[0] == \\\"run-node\\\" && variables.spec.containers[0].args[1]\
    \ == \\\"--controller\\\" && variables.spec.containers[0].args[3] == \\\"--run\\\" && variables.spec.containers[0].args[5]\
    \ == \\\"--generation\\\" && variables.spec.containers[0].args[7] == \\\"--module-cache\\\" && variables.spec.containers[0].args[8]\
    \ == \\\"/tmp/modules\\\" && variables.spec.containers[0].args[9] == \\\"--bootstrap-file\\\" && variables.spec.containers[0].args[10]\
    \ == \\\"/etc/sparkwing/bootstrap/bootstrap\\\"\",\n      \"message\": \"the workload containers run only the\
    \ launcher's sparkwing-runner commands\"\n    }\n  }\n]"
```

## Storage and behavior

Prepared records belong to their run for retention and deletion. Existing
source/byte billing and quotas apply to transfers and retained bytes. No new
setting, bucket, IAM grant, credential or cross-run cache is required. The
unreleased schema baseline 28 must include the prepared record kind.

An absent artifact or changed build inputs rebuilds from sealed source. Failed
size/SHA-256 verification stops the job before restored code runs. Rollback
restores the previous launcher and admission shape together.
