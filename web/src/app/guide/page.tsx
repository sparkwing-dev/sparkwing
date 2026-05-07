"use client";

import { useState } from "react";

const sections = [
  {
    id: "overview",
    title: "Testing overview",
    content: `
## How to test every Sparkwing feature

This guide walks through testing the entire Sparkwing stack running in your kind cluster. Each section covers one feature area with exact commands you can run right now.

**Prerequisites:**
- Kind cluster running (\`sparkwing cluster status\`)
- Port forwards active (\`./bin/port-forward.sh\`)
- Controller reachable at \`http://localhost:9001\`

**Quick health check:**

\`\`\`bash
# Verify the cluster
kubectl get pods -n sparkwing

# Verify the controller API
curl -s http://localhost:9001/health

# Verify agents are connected
curl -s http://localhost:9001/agents | jq .
\`\`\`

If all pods are Running and the controller returns healthy, you're good to go.
`,
  },
  {
    id: "trigger",
    title: "1. Trigger a build",
    content: `
## Trigger a build via API

The most basic operation — trigger a job and watch it appear in the dashboard.

\`\`\`bash
# Trigger a job for okbot-go
curl -X POST "http://localhost:9001/trigger?pipeline=okbot-go"
\`\`\`

**What to verify:**
- Job appears in the Workflows page within seconds
- Status transitions: pending -> claimed -> running -> complete/failed
- The listener claims the job (check Agents page)

## Trigger with environment variables

\`\`\`bash
curl -X POST "http://localhost:9001/trigger?pipeline=okbot-go&env.BRANCH=main&env.CUSTOM_VAR=hello"
\`\`\`

## Trigger with routing preferences

\`\`\`bash
# Prefer a specific agent type
curl -X POST "http://localhost:9001/trigger?pipeline=okbot-go&prefer=type:listener"

# Require specific labels
curl -X POST "http://localhost:9001/trigger?pipeline=okbot-go&require=cluster:sparkwing"
\`\`\`

## Trigger from the Debug page

Go to the **Debug** tab in this dashboard. You can:
- Pick a pipeline from the quick trigger buttons
- Set custom environment variables
- Set prefer/require routing
- See the raw JSON response

## Trigger via CLI

\`\`\`bash
sparkwing pipeline run --name okbot-go
\`\`\`
`,
  },
  {
    id: "pipeline-viz",
    title: "2. Pipeline visualization",
    content: `
## View pipeline jobs in the dashboard

After triggering a build, the Workflows page shows a visual job breakdown.

**To see pipeline visualization:**
1. Trigger a build for a pipeline that spawns child jobs
2. Go to the **Workflows** page
3. Click on the job row to expand details
4. The pipeline view shows jobs as sequential entries, with spawned children shown as concurrent rows

**What to verify:**
- Jobs show as sequential entries with step names
- Spawned children show as concurrent rows
- Green = passed, Red = failed, Gray = skipped
- Duration is shown per job and total

## Test with a pipeline that spawns children

Define in \`.sparkwing/pipelines.yaml\`:

\`\`\`yaml
pipeline-test:
  root: pipeline-test
\`\`\`

Create the job at \`.sparkwing/jobs/pipeline_test.go\`:

\`\`\`go
package jobs

import "github.com/sparkwing-dev/sparkwing/pkg/sparkwing"

func JobPipelineTest() {
    sparkwing.RunStep(step.Shell("fmt", "echo 'formatting...'"))
    sparkwing.RunStep(step.Shell("vet", "echo 'vetting...'"))
    sparkwing.RunStep(step.Shell("test", "echo 'running tests...'"))
    sparkwing.RunStep(step.Shell("build", "echo 'building...'"))
}
\`\`\`

Register in \`main.go\`:

\`\`\`go
sparkwing.Register("pipeline-test", jobs.JobPipelineTest)
\`\`\`
`,
  },
  {
    id: "logs",
    title: "3. Live log streaming",
    content: `
## Real-time log output

Sparkwing streams logs from running jobs to the dashboard via Server-Sent Events (SSE).

**To test:**
1. Trigger a build that takes a few seconds (e.g., a real Docker build)
2. Go to **Workflows** page
3. Click on a running job
4. Watch logs stream in real-time with line numbers

**What to verify:**
- Logs appear as the job runs (not just at the end)
- Line numbers are shown
- Keywords like PASS/FAIL are color-highlighted
- Auto-scroll keeps you at the bottom
- Logs persist after the job completes

## Test log streaming with a slow job

Create a job that outputs lines slowly:

\`\`\`go
func JobLogTest() {
    sparkwing.RunStep(step.Shell("slow-output",
        "for i in $(seq 1 20); do echo \\"Line $i: processing...\\"; sleep 1; done"))
}
\`\`\`

Register it and add a pipeline entry:

\`\`\`yaml
log-test:
  root: log-test
\`\`\`

## View logs via API

\`\`\`bash
# SSE stream (curl will show lines as they arrive)
curl -N http://localhost:9001/logs/<JOB_ID>
\`\`\`
`,
  },
  {
    id: "agents",
    title: "4. Agents & capacity",
    content: `
## Agent monitoring

The **Agents** page shows all connected listeners and their capacity.

**What to verify:**
- Agent name, type, and labels are displayed
- Capacity bar shows used/total slots
- Active jobs list updates in real-time
- Agents that disconnect are removed after 5 minutes

## Check agents via API

\`\`\`bash
curl -s http://localhost:9001/agents | jq .
\`\`\`

Expected output:

\`\`\`json
[
  {
    "name": "listener-0",
    "type": "listener",
    "labels": {"cluster": "sparkwing", "arch": "arm64"},
    "status": "idle",
    "max_concurrent": 3,
    "active_jobs": []
  }
]
\`\`\`

## Test capacity limits

The listener is configured with \`MAX_CONCURRENT=3\`. Trigger 4+ jobs rapidly:

\`\`\`bash
for i in 1 2 3 4 5; do
  curl -s -X POST "http://localhost:9001/trigger?pipeline=okbot-go&env.RUN=$i" &
done
wait
\`\`\`

Watch the Agents page — the 4th and 5th jobs should queue until a slot opens.

## Run a local agent

\`\`\`bash
# Connect your laptop as an additional agent
sparkwing listen -m 2
\`\`\`

Your local machine should appear in the Agents page.
`,
  },
  {
    id: "breakpoints",
    title: "5. Breakpoints & approval",
    content: `
## Step-level breakpoints

\`step.RequireApproval()\` pauses the job before continuing, letting you inspect the environment.

**Create a test job:**

\`\`\`go
func JobBreakTest() {
    sparkwing.RunStep(step.Shell("compile", "echo 'compiled'"))
    sparkwing.RunStep(step.RequireApproval("deploy-gate"))
    sparkwing.RunStep(step.Shell("deploy", "echo 'deployed'"))
}
\`\`\`

**Test flow:**
1. Trigger the build
2. Watch it pause at the approval gate (status: paused)
3. In the **Workflows** page, the job shows a "Continue" button
4. Click Continue (or use CLI below)
5. The deploy step runs

**Resume via CLI:**

\`\`\`bash
sparkwing jobs continue --job <JOB_ID>
\`\`\`

**Resume via API:**

\`\`\`bash
curl -X POST http://localhost:9001/jobs/<JOB_ID>/breakpoint-continue
\`\`\`

## Approval with timeout

\`\`\`go
step.RequireApprovalWithTimeout("production", 1 * time.Hour)
\`\`\`

The job pauses until approved. 24-hour timeout by default, or specify a custom timeout.

**Check breakpoint status:**

\`\`\`bash
curl -s http://localhost:9001/jobs/<JOB_ID>/breakpoint | jq .
\`\`\`
`,
  },
  {
    id: "cancel-retry",
    title: "6. Cancel & retry",
    content: `
## Cancel a running job

\`\`\`bash
# Via CLI
sparkwing jobs cancel --job <JOB_ID>

# Via API
curl -X POST http://localhost:9001/jobs/<JOB_ID>/cancel
\`\`\`

**What to verify:**
- Job status changes to "cancelled" in the dashboard
- The runner pod is cleaned up
- The job appears as cancelled in the Workflows page

## Retry a failed job

\`\`\`bash
# Via CLI
sparkwing jobs retry --job <JOB_ID>

# Via API
curl -X POST "http://localhost:9001/trigger?pipeline=<PIPELINE>&retry_of=<JOB_ID>"
\`\`\`

**What to verify:**
- A new job is created with the same pipeline/config
- The original job stays in "failed" state
- The new job picks up from scratch

## Test with a deliberately failing job

\`\`\`go
func JobFailTest() {
    sparkwing.RunStep(step.Shell("fail", "exit 1"))
}
\`\`\`

Trigger it, let it fail, then retry.
`,
  },
  {
    id: "spawn-matrix",
    title: "7. Spawn & matrix",
    content: `
## Spawn child jobs

Spawn distributes work across all available listeners through the controller queue.

\`\`\`go
package jobs

import (
    "github.com/sparkwing-dev/sparkwing/pkg/sparkwing"
)

func JobSpawnTest() {
    shards := []string{"0", "1", "2"}
    sparkwing.SpawnAll("shards", shards,
        sparkwing.SpawnPipeline("okbot-go-test"),
        sparkwing.WithEnv(func(val string) map[string]string {
            return map[string]string{"SHARD": val}
        }),
    )
}
\`\`\`

**What to verify in the dashboard:**
- Parent job shows as running
- 3 child jobs appear with \`parent_id\` set
- Children run in parallel (check Agents capacity)
- Parent waits for all children, then completes
- Workflows page shows parent->child hierarchy

## SpawnMatrix

\`\`\`go
sparkwing.SpawnMatrix("test-matrix",
    sparkwing.Axis{"VERSION", []string{"1", "2"}},
    sparkwing.Axis{"MODE", []string{"fast", "slow"}},
)
// Creates 4 children: VERSION=1+MODE=fast, VERSION=1+MODE=slow, etc.
\`\`\`

**What to verify:**
- 4 child jobs are created (2x2 cartesian product)
- Each child has the correct env vars set
- All 4 run and complete independently

## Concurrency limits

\`\`\`go
sparkwing.SpawnAll("deploy", deployTargets,
    sparkwing.SpawnPipeline("deploy-prod"),
    sparkwing.MaxConcurrent(1),
)
\`\`\`

Only one child runs at a time. The next waits for the previous to finish.
`,
  },
  {
    id: "docker",
    title: "8. Docker builds",
    content: `
## Build and push Docker images

The Docker-in-Docker service runs in the cluster for building container images.

\`\`\`bash
# Verify DinD is running
docker -H tcp://localhost:9003 info
\`\`\`

## Test a Docker build job

\`\`\`go
import "github.com/sparkwing-dev/sparks-core/docker"

func JobDockerTest() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "okbot-go",
        Dockerfile: "okbot-go/Dockerfile",
        Context:    "okbot-go",
        Registries: registries,
    })
}
\`\`\`

**What to verify:**
- Image builds successfully using the DinD service
- Image is pushed to the in-cluster registry (\`localhost:30500\`)
- Build logs stream to the dashboard

## Test DockerRun

\`\`\`go
sparkwing.RunStep(step.DockerRun("test", "alpine:3.21",
    []string{"sh", "-c", "echo hello from container"},
    step.WithEnv("MY_VAR", "my_value"),
))
\`\`\`

## Docker build options

\`\`\`go
// Build from subdirectory with custom platform
docker.BuildAndPush(docker.BuildConfig{
    Image:      "myapp",
    Dockerfile: "services/myapp/Dockerfile",
    Context:    "services/myapp",
    Platform:   "linux/arm64",
    Registries: registries,
})

// Run with volume mounts
sparkwing.RunStep(step.DockerRun("test", "node:22", []string{"npm", "test"},
    step.MountWorkDir("myapp"),
    step.WithVolume("node_modules", "/app/node_modules"),
    step.WithWorkdir("/app"),
))
\`\`\`
`,
  },
  {
    id: "kube",
    title: "9. Deploying",
    content: `
## Deploying

\`deploy.Run()\` auto-detects the environment. In a K8s cluster it pushes to gitops + syncs ArgoCD. Locally it restarts deployments via kubectl.

\`\`\`go
import "github.com/sparkwing-dev/sparks-core/deploy"

deploy.Run(deploy.Config{
    AppName:   "okbot-go",
    Namespace: "okbot",
    Images:    []string{"okbot-go"},
})
\`\`\`

**Test it:**

\`\`\`bash
# First verify the deployment exists
kubectl get deploy -n okbot

# Trigger via a pipeline that does: BuildAndPush -> deploy.Run
curl -X POST "http://localhost:9001/trigger?pipeline=okbot-go"
\`\`\`

**What to verify:**
- The deployment restarts (\`kubectl rollout status deploy/okbot-go -n okbot\`)
- New pods pull the latest image
- The app is accessible on its port-forward

## Rollback

Use \`kubectl rollout undo\` for manual rollbacks:

\`\`\`bash
kubectl rollout undo deploy/okbot-go -n okbot
\`\`\`

## Deploy with error handling

Use a custom step that handles deploy errors:

\`\`\`go
func JobDeployWithRollback() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "okbot-go",
        Dockerfile: "okbot-go/Dockerfile",
        Context:    "okbot-go",
        Registries: registries,
    })
    sparkwing.RunStep("deploy", func() error {
        deploy.Run(deploy.Config{
            AppName:   "okbot-go",
            Namespace: "okbot",
            Images:    []string{"okbot-go"},
        })
        return nil
    })
}
\`\`\`

## Verify rollback

\`\`\`bash
# Check deployment revision history
kubectl rollout history deploy/okbot-go -n okbot

# Manually trigger a rollback to confirm it works
kubectl rollout undo deploy/okbot-go -n okbot
\`\`\`
`,
  },
  {
    id: "artifacts",
    title: "10. Artifacts",
    content: `
## Upload artifacts

\`\`\`go
func JobArtifactTest() {
    sparkwing.RunStep(step.Shell("build", "echo 'build output' > dist/output.txt"))
    sparkwing.RunStep(step.UploadArtifact("dist/*.txt"))
}
\`\`\`

Artifacts are stored in the git cache service and keyed by job ID.

## Download artifacts in another job

\`\`\`go
func JobDownloadTest() {
    sparkwing.RunStep(step.DownloadArtifact("parent-job-id", "dist/*.txt"))
    sparkwing.RunStep(step.Shell("verify", "cat dist/output.txt"))
}
\`\`\`

## Test artifact flow with spawn

The typical pattern is: parent spawns children, children upload artifacts, parent downloads them.

\`\`\`go
func JobBuildAll() {
    sparkwing.SpawnAll("build", []string{"linux", "darwin"},
        sparkwing.SpawnPipeline("build-platform"),
        sparkwing.WithEnv(func(val string) map[string]string {
            return map[string]string{"PLATFORM": val}
        }),
    )
}
\`\`\`

**What to verify:**
- Artifacts upload to gitcache successfully
- Artifacts can be downloaded by job ID
- Glob patterns match correctly
`,
  },
  {
    id: "dedup",
    title: "11. Deduplication",
    content: `
## Content-addressed caching

The dedup step prevents redundant work across runners.

\`\`\`go
func JobDedupTest() {
    sparkwing.RunStep(step.Deduplicate("test:"+sparkwing.RunConfig.Commit,
        step.Shell("test", "go test ./..."),
    ))
}
\`\`\`

**Test it:**
1. Trigger a build that uses Deduplicate
2. Trigger the same build again immediately
3. The second build should show "already running" and wait
4. When the first completes, the second inherits the result

## Test with the E2E dedup test

\`\`\`bash
./bin/e2e-dedup-test.sh
\`\`\`

**What to verify:**
- First runner claims ownership ("dedup: claimed ...")
- Second runner waits ("dedup: already running ...")
- Second runner inherits success ("dedup: completed by owner ...")
- HMAC signatures prevent cache tampering

## Tree hash caching

The git cache service computes tree hashes. If the code hasn't changed between commits, the hash is the same and the build is skipped.

\`\`\`bash
# Check tree hash for a repo
curl -s http://localhost:9001/jobs | jq '.[0].cache_key'
\`\`\`
`,
  },
  {
    id: "conditional",
    title: "12. Conditional logic",
    content: `
## Branch-based conditions

Jobs are plain Go — use normal control flow:

\`\`\`go
func JobConditional() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })

    if sparkwing.RunConfig.Branch == "main" {
        deploy.Run(deploy.Config{
            AppName:   "myapp",
            Namespace: "prod",
            Images:    []string{"myapp"},
        })
    }
}
\`\`\`

**Test it:**

\`\`\`bash
# This should skip the deploy step
curl -X POST "http://localhost:9001/trigger?pipeline=myapp&env.BRANCH=feature"

# This should run the deploy step
curl -X POST "http://localhost:9001/trigger?pipeline=myapp&env.BRANCH=main"
\`\`\`

**What to verify in the dashboard:**
- When condition is false, the deploy step is skipped
- Subsequent steps still run
- Job result reflects the actual steps executed

## Timeouts

Use a context with timeout in a custom step:

\`\`\`go
func JobTimeoutTest() {
    sparkwing.RunStep("slow", func() error {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        // long-running work with ctx...
        return ctx.Err()
    })
}
\`\`\`

**What to verify:**
- Job fails after the timeout with an error
- The timed-out step is reported as failed
`,
  },
  {
    id: "retry-step",
    title: "13. Retry & services",
    content: `
## Retry with exponential backoff

\`\`\`go
// Retry 3 times with 1s, 2s, 4s delays
sparkwing.RunStep(step.Retry(3, step.Shell("flaky", "exit 1")))
\`\`\`

**What to verify in logs:**
- "retry 1/3 after 1s"
- "retry 2/3 after 2s"
- "retry 3/3 after 4s"
- "failed after 3 retries"

## Conditional retry

\`\`\`go
sparkwing.RunStep(step.RetryWith(step.RetryOpts{
    Count: 3,
    Delay: 2 * time.Second,
    OnlyIf: func(err error) bool {
        return strings.Contains(err.Error(), "timeout")
    },
}, step.Shell("deploy", "echo deploy")))
\`\`\`

## Service containers

Spin up databases/caches as sidecars:

\`\`\`go
sparkwing.WithServices(
    step.Service("postgres:15", 5432).
        Env("POSTGRES_PASSWORD", "test").
        ReadyCmd("pg_isready -h localhost -p 5432"),
    step.Service("redis:7", 6379),
).RunStep(
    step.Shell("test", "echo 'postgres and redis are running'"),
)
\`\`\`

**What to verify:**
- Service containers start before the step
- Health check waits for readiness
- Services are accessible on localhost:{port}
- Containers are cleaned up after the step
`,
  },
  {
    id: "secrets",
    title: "14. Secrets management",
    content: `
## Set secrets

\`\`\`bash
# Set a secret for the production environment
sparkwing secret set --env production --key DB_PASSWORD --value "s3cret"
sparkwing secret set --env production --key API_KEY --value "key123"
\`\`\`

## List secrets

\`\`\`bash
sparkwing secret list --env production
\`\`\`

## Delete secrets

\`\`\`bash
sparkwing secret delete --env production --key API_KEY
\`\`\`

## Secrets via API

\`\`\`bash
# Set
curl -X POST http://localhost:9001/secrets \
  -H "Content-Type: application/json" \
  -d '{"environment":"production","key":"DB_PASSWORD","value":"s3cret"}'

# List
curl -s http://localhost:9001/secrets?environment=production | jq .

# Delete
curl -X DELETE "http://localhost:9001/secrets?environment=production&key=DB_PASSWORD"
\`\`\`

**What to verify:**
- Secrets are stored encrypted in SQLite
- Secrets are injected as env vars in runner pods
- Secret values are never logged
`,
  },
  {
    id: "tests-junit",
    title: "15. Test results",
    content: `
## Test results

Run tests as a shell step. Test output is parsed from the runner logs automatically.

\`\`\`go
func JobTestResults() {
    sparkwing.RunStep(step.Shell("test", "go test -v ./..."))
}
\`\`\`

**What to verify:**
- Test output is captured in job logs
- Results visible in the **Tests** page in the dashboard
- Pass rates tracked per pipeline

## Flaky test detection

The **Tests** page shows flaky test reports. A test is considered flaky if it has inconsistent results across recent runs.

**Generate flaky test data:**

\`\`\`bash
# Run the flaky detection script
./bin/flaky-detect.sh 10
\`\`\`

Check the Tests page for flaky test reports.

## View test results via API

\`\`\`bash
# Job results include JUnit data in the pipeline_result
curl -s http://localhost:9001/jobs/<JOB_ID> | jq '.result.pipeline_result'
\`\`\`
`,
  },
  {
    id: "webhooks",
    title: "16. GitHub webhooks",
    content: `
## Webhook setup

Sparkwing receives GitHub push/PR events and triggers builds automatically.

**Required environment:**
- \`GITHUB_WEBHOOK_SECRET\` — HMAC signing secret (must match GitHub)
- \`GITHUB_TOKEN\` — for posting PR status checks (optional)

## Test webhook delivery

\`\`\`bash
# Simulate a GitHub push webhook
SECRET="your-webhook-secret"
PAYLOAD='{"ref":"refs/heads/main","repository":{"clone_url":"https://github.com/you/repo.git","full_name":"you/repo"},"head_commit":{"id":"abc123"}}'

SIGNATURE=$(echo -n "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/.*= //')

curl -X POST http://localhost:9001/webhook/github \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-Hub-Signature-256: sha256=$SIGNATURE" \
  -d "$PAYLOAD"
\`\`\`

**What to verify:**
- Valid signatures trigger a build
- Invalid/missing signatures are rejected (403)
- Repo URL is validated against allowlist (if set)
- Git ref is sanitized
- The correct pipeline is selected based on \`pipelines.yaml\` trigger rules

## Branch enforcement

Deploys only proceed if the build is on an allowed branch:

\`\`\`bash
# Set SPARKWING_ALLOWED_REPO_HOSTS to restrict repos
# Branch conditions are handled in the job function itself
\`\`\`
`,
  },
  {
    id: "security",
    title: "17. Security hardening",
    content: `
## Run the security test suite

\`\`\`bash
./bin/security-test.sh
\`\`\`

This tests:
1. **Auth middleware** — GET and POST endpoints reject unauthenticated requests
2. **Webhook signatures** — unsigned payloads are rejected
3. **Repo URL validation** — malicious URLs are blocked
4. **Git ref sanitization** — injection attempts in branch names are blocked
5. **YAML injection** — env var values can't inject YAML into K8s manifests
6. **Path traversal** — script paths can't escape the work directory

## Test API auth

\`\`\`bash
# Without token (should fail if SPARKWING_API_TOKEN is set)
curl -s http://localhost:9001/jobs

# With token
curl -s -H "Authorization: Bearer <token>" http://localhost:9001/jobs
\`\`\`

## Test rate limiting

\`\`\`bash
# Trigger endpoint is limited to 20 req/min per IP
for i in $(seq 1 25); do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:9001/trigger?pipeline=test")
  echo "Request $i: $STATUS"
done
# Requests 21+ should return 429 Too Many Requests
\`\`\`

## Verify container security

\`\`\`bash
# Check non-root
kubectl exec -n sparkwing deploy/sparkwing-controller -- id
# Should show uid=65534

# Check read-only filesystem
kubectl exec -n sparkwing deploy/sparkwing-controller -- touch /test 2>&1
# Should fail with "Read-only file system"

# Check network policies
kubectl get networkpolicy -n sparkwing
\`\`\`

## Audit log

\`\`\`bash
curl -s http://localhost:9001/audit | jq .
\`\`\`

Shows all trigger events with timestamps, IPs, pipelines, and results.
`,
  },
  {
    id: "metrics",
    title: "18. Metrics & logging",
    content: `
## Prometheus metrics

\`\`\`bash
curl -s http://localhost:9001/metrics
\`\`\`

Exposes job counts, queue depth, agent status, and more.

## Structured logging

The controller outputs structured JSON logs when \`SPARKWING_LOG_FORMAT=json\`:

\`\`\`bash
kubectl logs -n sparkwing deploy/sparkwing-controller --tail=20
\`\`\`

## Build badges

\`\`\`bash
# Get a build badge for a pipeline
curl -s http://localhost:9001/badge/okbot-go
# Returns SVG badge showing latest build status
\`\`\`

## Log retention

Completed jobs older than 2 hours are evicted from memory but kept in SQLite. Query historical jobs:

\`\`\`bash
curl -s "http://localhost:9001/jobs?limit=50&offset=0" | jq 'length'
\`\`\`
`,
  },
  {
    id: "plugins",
    title: "19. Plugins",
    content: `
## Available plugins

| Plugin | Step | Env var |
|--------|------|---------|
| **slack** | \`slack.Notify(channel, msg)\` | \`SLACK_WEBHOOK_URL\` |
| **s3** | \`s3.Upload(bucket, prefix, patterns...)\` | AWS creds from env/IAM |

## Test a plugin (Slack example)

\`\`\`go
import (
    "github.com/sparkwing-dev/sparkwing/plugins/slack"
    "github.com/sparkwing-dev/sparks-core/docker"
    "github.com/sparkwing-dev/sparks-core/deploy"
)

func JobDeployWithNotify() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })
    deploy.Run(deploy.Config{
        AppName:   "myapp",
        Namespace: "prod",
        Images:    []string{"myapp"},
    })
    sparkwing.RunStep(slack.Notify("#deploys", "myapp deployed"))
}
\`\`\`

If \`SLACK_WEBHOOK_URL\` is not set, the plugin logs a skip message instead of failing.

## Error notifications

\`\`\`go
func JobDeployWithAlerts() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })
    sparkwing.RunStep("deploy-and-notify", func() error {
        deploy.Run(deploy.Config{
            AppName:   "myapp",
            Namespace: "prod",
            Images:    []string{"myapp"},
        })
        sparkwing.RunStep(slack.Notify("#deploys", "myapp deployed successfully"))
        return nil
    })
}
\`\`\`
`,
  },
  {
    id: "gitcache",
    title: "20. Git cache",
    content: `
## Git cache service

The git cache clones repos once and serves them as tarballs. This eliminates redundant git clones across runners.

\`\`\`bash
# Health check
curl -s http://localhost:9001/health

# Check cached repos (via gitcache directly)
kubectl exec -n sparkwing deploy/sparkwing-gitcache -- ls /data/repos/
\`\`\`

## Archive endpoint

\`\`\`bash
# Request a repo archive
curl -s "http://<gitcache>:8090/archive?repo=https://github.com/you/repo.git&branch=main" -o repo.tar
\`\`\`

The git cache:
- Clones the repo on first request
- Fetches on subsequent requests
- Locks per-repo to prevent duplicate clones
- Supports SSH keys for private repos

## Artifact storage

Artifacts are also stored in the git cache service:

\`\`\`bash
kubectl exec -n sparkwing deploy/sparkwing-gitcache -- ls /data/artifacts/
\`\`\`
`,
  },
  {
    id: "hooks",
    title: "21. Hooks",
    content: `
## Lifecycle hooks

Hooks are shell scripts that run at specific points in the job lifecycle.

| Hook | When |
|------|------|
| \`pre-checkout\` | Before git pull |
| \`post-checkout\` | After git pull |
| \`pre-command\` | Before job runs |
| \`post-command\` | After job (pass or fail) |
| \`pre-exit\` | Always, during cleanup |

## Test a repo hook

Create \`.sparkwing/hooks/post-command\`:

\`\`\`bash
#!/bin/bash
echo "Hook: job $SPARKWING_JOB_ID finished with status $SPARKWING_COMMAND_EXIT_STATUS"
if [ "$SPARKWING_COMMAND_EXIT_STATUS" != "0" ]; then
    echo "JOB FAILED for $SPARKWING_PIPELINE"
fi
\`\`\`

Make it executable: \`chmod +x .sparkwing/hooks/post-command\`

**What to verify in job logs:**
- Hook output appears after the job completes
- Environment variables are populated correctly

## Agent hooks

Place scripts in \`~/.config/sparkwing/hooks/\`. Agent hooks run before repo hooks.
`,
  },
  {
    id: "local-dev",
    title: "22. Local development",
    content: `
## Run pipelines locally

You don't need a cluster to develop and test pipelines:

\`\`\`bash
cd your-project
sparkwing pipeline run --name myapp
\`\`\`

This compiles and runs the pipeline locally. Spawn creates subprocesses instead of K8s pods.

## Local spawn

When running locally, \`sparkwing.SpawnAll()\` uses an embedded mini-controller:

\`\`\`go
func JobLocalTest() {
    sparkwing.SpawnAll("test", []string{"0", "1"},
        sparkwing.SpawnPipeline("test-runner"),
        sparkwing.WithEnv(func(val string) map[string]string {
            return map[string]string{"SHARD": val}
        }),
    )
    // Children run as local subprocesses
}
\`\`\`

## Port forwards summary

\`\`\`bash
./bin/port-forward.sh
\`\`\`

| Port | Service |
|------|---------|
| 9001 | Controller API |
| 9002 | Web Dashboard |
| 9003 | Docker-in-Docker |
| 9004 | Container Registry |
| 9050-9058 | Okbot app instances |

## Status check

\`\`\`bash
./bin/status.sh
\`\`\`

Shows running agents and queued jobs.
`,
  },
  {
    id: "e2e",
    title: "23. Full E2E tests",
    content: `
## Run the full test suite

\`\`\`bash
# Unit + integration tests (157 tests)
./bin/test.sh

# End-to-end tests (13 tests)
./bin/e2e-test.sh

# Dedup-specific E2E tests
./bin/e2e-dedup-test.sh

# Security tests (6 tests)
./bin/security-test.sh

# Flaky test detection (runs tests N times)
./bin/flaky-detect.sh 20

# Coverage report
./bin/coverage.sh
\`\`\`

## Pre-release validation

\`\`\`bash
./bin/pre-release-test.sh
\`\`\`

Runs everything: unit, integration, E2E, security, and flaky detection.

## Manual smoke test checklist

1. Trigger a build from the Debug page
2. Watch logs stream in real-time on Workflows page
3. Verify pipeline visualization shows jobs
4. Check Agents page shows connected listener
5. Check Tests page for test results
6. Trigger 5 builds rapidly — verify rate limiting at 20/min
7. Test an approval gate — pause and continue
8. Test cancel on a running job
9. Test retry on a failed job
10. Verify the Home page stats update
`,
  },
];

export default function GuidePage() {
  const [active, setActive] = useState("overview");
  const section = sections.find((s) => s.id === active);

  return (
    <div className="flex-1 flex overflow-hidden">
      {/* Sidebar */}
      <div className="w-60 border-r border-[var(--border)] bg-[var(--surface)] overflow-y-auto p-3">
        <div className="text-xs text-[var(--muted)] uppercase tracking-wider mb-3 px-2">
          Developer Testing Guide
        </div>
        {sections.map((s) => (
          <button
            key={s.id}
            onClick={() => setActive(s.id)}
            className={`block w-full text-left px-3 py-1.5 rounded text-sm mb-0.5 ${
              active === s.id
                ? "bg-[var(--accent)]/15 text-[var(--foreground)]"
                : "text-[var(--muted)] hover:text-[var(--foreground)] hover:bg-[var(--surface-raised)]"
            }`}
          >
            {s.title}
          </button>
        ))}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-8 max-w-3xl">
        {section && <MarkdownContent content={section.content} />}
      </div>
    </div>
  );
}

// Simple markdown renderer for code blocks, headers, tables, inline code, and bold
function MarkdownContent({ content }: { content: string }) {
  const lines = content.trim().split("\n");
  const elements: React.ReactNode[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Code blocks
    if (line.startsWith("```")) {
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) {
        codeLines.push(lines[i]);
        i++;
      }
      i++; // skip closing ```
      elements.push(
        <pre
          key={elements.length}
          className="bg-[#0d1117] border border-[var(--border)] rounded-lg p-4 overflow-x-auto mb-4 text-sm"
        >
          <code className="text-[var(--foreground)]">
            {codeLines.join("\n")}
          </code>
        </pre>,
      );
      continue;
    }

    // Headers
    if (line.startsWith("## ")) {
      elements.push(
        <h2 key={elements.length} className="text-lg font-bold mt-6 mb-3">
          {line.slice(3)}
        </h2>,
      );
      i++;
      continue;
    }

    // Tables
    if (line.startsWith("|")) {
      const tableLines: string[] = [];
      while (i < lines.length && lines[i].startsWith("|")) {
        tableLines.push(lines[i]);
        i++;
      }
      elements.push(<MarkdownTable key={elements.length} lines={tableLines} />);
      continue;
    }

    // Empty lines
    if (line.trim() === "") {
      i++;
      continue;
    }

    // Regular paragraph
    elements.push(
      <p
        key={elements.length}
        className="text-sm text-[var(--foreground)] mb-3 leading-relaxed"
      >
        <InlineFormat text={line} />
      </p>,
    );
    i++;
  }

  return <>{elements}</>;
}

function InlineFormat({ text }: { text: string }) {
  // Split on both inline code and bold
  const parts = text.split(/(`[^`]+`|\*\*[^*]+\*\*)/g);
  return (
    <>
      {parts.map((part, i) => {
        if (part.startsWith("`") && part.endsWith("`")) {
          return (
            <code
              key={i}
              className="bg-[var(--background)] px-1.5 py-0.5 rounded text-xs font-mono text-cyan-400"
            >
              {part.slice(1, -1)}
            </code>
          );
        }
        if (part.startsWith("**") && part.endsWith("**")) {
          return (
            <strong key={i} className="font-semibold text-[var(--foreground)]">
              {part.slice(2, -2)}
            </strong>
          );
        }
        return <span key={i}>{part}</span>;
      })}
    </>
  );
}

function MarkdownTable({ lines }: { lines: string[] }) {
  if (lines.length < 2) return null;
  const headers = lines[0]
    .split("|")
    .filter(Boolean)
    .map((s) => s.trim());
  const rows = lines.slice(2).map((l) =>
    l
      .split("|")
      .filter(Boolean)
      .map((s) => s.trim()),
  );

  return (
    <div className="overflow-x-auto mb-4">
      <table className="w-full text-sm border border-[var(--border)] rounded-lg">
        <thead>
          <tr className="border-b border-[var(--border)] bg-[var(--surface)]">
            {headers.map((h, i) => (
              <th
                key={i}
                className="px-3 py-2 text-left text-xs text-[var(--muted)] font-medium"
              >
                <InlineFormat text={h} />
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, ri) => (
            <tr key={ri} className="border-b border-[var(--border)]">
              {row.map((cell, ci) => (
                <td key={ci} className="px-3 py-2">
                  <InlineFormat text={cell} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
