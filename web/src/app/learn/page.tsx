"use client";

import { useState } from "react";

const sections = [
  {
    id: "quickstart",
    title: "Quick start",
    content: `
## Get running in 3 commands

\`\`\`bash
# 1. Create a local cluster with the full sparkwing stack
sparkwing cluster create --name dev

# 2. Set up your repo
cd your-project
sparkwing pipeline init

# 3. Run your first pipeline
sparkwing pipeline run --name your-project
\`\`\`

That's it. You now have a controller, listener, web dashboard, git cache, and Docker-in-Docker — all running locally in a kind cluster.

## What sparkwing pipeline init creates

\`\`\`
.sparkwing/
  pipelines.yaml    # pipeline configs (triggers, env, root job)
  main.go           # Register jobs + RunPipeline
  go.mod
  jobs/             # job functions (JobXxx naming)
\`\`\`

## The model

A **pipeline** is a YAML entry in \`pipelines.yaml\` that maps triggers to a root **job**. A job is a Go function that calls \`sparkwing.RunStep()\` to execute **steps** and \`sparkwing.Spawn()\` to distribute work to other runners.

\`\`\`yaml
# .sparkwing/pipelines.yaml
myapp:
  on:
    push:
      branches: [main]
  root: myapp
\`\`\`

\`\`\`go
// .sparkwing/jobs/myapp.go
package jobs

import (
    "github.com/sparkwing-dev/sparks-core/docker"
    "github.com/sparkwing-dev/sparks-core/deploy"
)

func JobMyapp() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })
    deploy.Run(deploy.Config{
        AppName:   "myapp",
        Namespace: "default",
        Images:    []string{"myapp"},
    })
}
\`\`\`

\`\`\`go
// .sparkwing/main.go
func main() {
    sparkwing.Register("myapp", jobs.JobMyapp)
    sparkwing.RunPipeline()
}
\`\`\`
`,
  },
  {
    id: "cli",
    title: "CLI reference",
    content: `
## Commands

| Command | Description |
|---------|-------------|
| \`sparkwing cluster create --name dev\` | Create a kind cluster with full stack |
| \`sparkwing cluster status\` | Show all clusters and their status |
| \`sparkwing cluster destroy --name dev\` | Tear down a cluster |
| \`sparkwing cluster config\` | Show cluster configuration |
| \`sparkwing cluster port-forward\` | Forward cluster ports to localhost |
| \`sparkwing pipeline init\` | Create .sparkwing/ in your repo |
| \`sparkwing pipeline new --name myapp\` | Add a new pipeline |
| \`sparkwing pipeline list\` | List all pipelines |
| \`sparkwing pipeline run --name myapp\` | Run a pipeline locally |
| \`sparkwing jobs list\` | List all jobs |
| \`sparkwing jobs cancel --job <ID>\` | Cancel a running job |
| \`sparkwing jobs retry --job <ID>\` | Retry a failed job |
| \`sparkwing jobs attach --job <ID>\` | Attach to a running job's logs |
| \`sparkwing jobs env --job <ID>\` | Show environment for a job |
| \`sparkwing jobs continue --job <ID>\` | Continue a paused job |
| \`sparkwing listen\` | Connect to a controller and run jobs |
| \`sparkwing listen -m 5\` | Listen with 5 concurrent slots |
| \`sparkwing secret set --env prod --key K --value V\` | Set a secret |
| \`sparkwing secret list --env prod\` | List secrets |
| \`sparkwing secret get --env prod --key K\` | Get a secret value |
| \`sparkwing secret delete --env prod --key K\` | Delete a secret |
`,
  },
  {
    id: "structure",
    title: "Project structure",
    content: `
## The .sparkwing/ directory

\`\`\`
.sparkwing/
  pipelines.yaml              # all pipeline configs (triggers, env, root job)
  main.go                     # Register("name", fn) + RunPipeline()
  go.mod                      # Go module for compilation
  jobs/                       # job functions (JobXxx naming convention)
    myapp.go
    myapp_test.go
  shared/                     # reusable code shared across jobs
    my_custom_step.go
  hooks/                      # lifecycle hooks (bash scripts)
    post-checkout
    post-command
\`\`\`

Each pipeline maps to a root job function. Jobs are plain Go functions registered in \`main.go\` via \`sparkwing.Register()\`.
`,
  },
  {
    id: "pipelines",
    title: "Writing pipelines",
    content: `
## pipelines.yaml

All pipeline configs live in a single file. Each entry defines triggers and a root job.

\`\`\`yaml
# .sparkwing/pipelines.yaml
myapp:
  on:
    push:
      branches: [main]
  root: myapp

myapp-test:
  root: myapp-test
\`\`\`

## Pipeline fields

| Field | Description |
|-------|-------------|
| \`on.push.branches\` | Trigger on push to these branches |
| \`on.pull_request\` | Trigger on PR events |
| \`root\` | Name of the registered job function to run |
| \`env\` | Default environment variables |

## main.go

Register all job functions and call \`RunPipeline()\`:

\`\`\`go
package main

import (
    "github.com/sparkwing-dev/sparkwing/pkg/sparkwing"
    ".sparkwing/jobs"
)

func main() {
    sparkwing.Register("myapp", jobs.JobMyApp)
    sparkwing.Register("myapp-test", jobs.JobMyAppTest)
    sparkwing.Register("default", jobs.JobDefault)
    sparkwing.RunPipeline()
}
\`\`\`

The controller tells the runner which registered job to execute based on the pipeline's \`root\` field.
`,
  },
  {
    id: "jobs",
    title: "Writing jobs",
    content: `
## Job functions

A job is a Go function in \`.sparkwing/jobs/\` that uses \`sparkwing.RunStep()\` to execute steps, and sparks-core packages for Docker builds and deploys.

\`\`\`go
package jobs

import (
    "github.com/sparkwing-dev/sparkwing/pkg/sparkwing"
    "github.com/sparkwing-dev/sparks-core/docker"
    "github.com/sparkwing-dev/sparks-core/deploy"
)

func JobMyapp() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })
    deploy.Run(deploy.Config{
        AppName:   "myapp",
        Namespace: "default",
        Images:    []string{"myapp"},
    })
}
\`\`\`

Build and deploy in one function. \`BuildAndPush\` handles building and pushing. \`deploy.Run\` auto-detects gitops vs kubectl.

## RunStep variants

\`\`\`go
// Run a custom function as a named step
sparkwing.RunStep("lint", func() {
    // custom logic here
})

// Run a function that can fail
sparkwing.RunStep("validate", func() error {
    if !isValid() {
        return fmt.Errorf("validation failed")
    }
    return nil
})
\`\`\`

## Runtime context

Access pipeline metadata at runtime:

\`\`\`go
sparkwing.RunConfig.Pipeline  // current pipeline name
sparkwing.RunConfig.Branch    // git branch
sparkwing.RunConfig.Commit    // git commit SHA
\`\`\`

## Conditional logic

Jobs are plain Go — use normal \`if\` statements:

\`\`\`go
func JobDeploy() {
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
`,
  },
  {
    id: "steps",
    title: "Built-in steps",
    content: `
## Available steps and packages

| Package / Step | Description |
|------|-------------|
| \`step.Shell(name, cmd)\` | Run a shell command |
| \`docker.BuildAndPush(cfg)\` | Build and push a Docker image (sparks-core) |
| \`step.DockerRun(name, image, cmd, opts...)\` | Run a command in a container |
| \`deploy.Run(cfg)\` | Deploy via gitops or kubectl (sparks-core) |
| \`step.RequireApproval(name)\` | Pause until approved |
| \`step.Retry(count, step)\` | Retry a step with exponential backoff |
| \`step.UploadArtifact(patterns...)\` | Upload files as artifacts |
| \`step.DownloadArtifact(jobID, patterns...)\` | Download artifacts from another job |
| \`step.GitPull(repo, branch)\` | Pull a git repository |

## Docker build + push

\`\`\`go
import "github.com/sparkwing-dev/sparks-core/docker"

docker.BuildAndPush(docker.BuildConfig{
    Image:      "myapp",
    Dockerfile: "Dockerfile",
    Context:    "services/myapp",  // build from a subdirectory
    Registries: []string{"localhost:30500"},
})
\`\`\`

## Deploy

\`\`\`go
import "github.com/sparkwing-dev/sparks-core/deploy"

deploy.Run(deploy.Config{
    AppName:   "myapp",
    Namespace: "default",
    Images:    []string{"myapp"},
})
\`\`\`

## Docker run options

\`\`\`go
sparkwing.RunStep(step.DockerRun("test", "ruby:3.3", []string{"ruby", "test.rb"},
    step.MountWorkDir("myapp"),                    // copy code into container
    step.WithVolume("gems", "/usr/local/bundle"),  // persistent cache
))
\`\`\`
`,
  },
  {
    id: "spawn",
    title: "Spawn & parallelism",
    content: `
## Spawning child jobs

\`sparkwing.Spawn()\` sends a single child job to another runner through the controller queue.

\`\`\`go
sparkwing.Spawn("deploy-staging")
\`\`\`

## SpawnAll

\`sparkwing.SpawnAll()\` sends multiple child jobs and waits for all to complete.

\`\`\`go
sparkwing.SpawnAll("tests", testShards,
    sparkwing.SpawnPipeline("myapp-test"),
    sparkwing.WithEnv(func(val string) map[string]string {
        return map[string]string{"TEST_NAME": val}
    }),
)
\`\`\`

Children are independent jobs. Any listener can claim them. The parent waits for all to complete.

## SpawnMatrix

Cartesian product spawn — expands axis combinations automatically:

\`\`\`go
sparkwing.SpawnMatrix("test",
    sparkwing.Axis{"GO_VERSION", []string{"1.24", "1.25"}},
    sparkwing.Axis{"OS", []string{"linux", "darwin"}},
)
// Creates 4 children: every GO_VERSION x OS combination
\`\`\`

## Spawn options

| Option | Description |
|--------|-------------|
| \`sparkwing.SpawnPipeline("name")\` | Which pipeline the child runs |
| \`sparkwing.WithEnv(fn)\` | Map each value to env vars |
| \`sparkwing.MaxConcurrent(n)\` | Limit concurrent children |
| \`sparkwing.Prefix("str")\` | Prefix child job names |
| \`sparkwing.SpawnPrefer(labels...)\` | Prefer runners with these labels |
| \`sparkwing.SpawnRequire(labels...)\` | Require runners with these labels |

## Local spawn

When running \`sparkwing pipeline run\` locally, spawn works without a controller — children run as subprocesses automatically.
`,
  },
  {
    id: "services",
    title: "Service containers",
    content: `
## WithServices

Spin up databases, caches, or other sidecar containers alongside a step:

\`\`\`go
sparkwing.WithServices(
    step.Service("postgres:15", 5432).
        Env("POSTGRES_PASSWORD", "test").
        ReadyCmd("pg_isready -h localhost -p 5432"),
    step.Service("redis:7", 6379),
).RunStep(
    step.Shell("test", "go test ./..."),
)
\`\`\`

Service containers start before the step, wait for readiness, and are cleaned up after the step completes.

## Multiple services

\`\`\`go
sparkwing.WithServices(
    step.Service("postgres:15", 5432).Env("POSTGRES_PASSWORD", "test"),
    step.Service("redis:7", 6379),
    step.Service("elasticsearch:8", 9200),
).RunStep(
    step.Shell("integration", "go test -tags=integration ./..."),
)
\`\`\`

Services are accessible on \`localhost:{port}\` from within the step.
`,
  },
  {
    id: "plugins",
    title: "Plugins",
    content: `
## Using plugins

Plugins are Go modules that provide additional steps. Add them to your \`.sparkwing/go.mod\`:

\`\`\`bash
cd .sparkwing && go get github.com/sparkwing-dev/sparkwing/plugins/slack
\`\`\`

Then use in your job:

\`\`\`go
import (
    "github.com/sparkwing-dev/sparkwing/plugins/slack"
    "github.com/sparkwing-dev/sparkwing/plugins/s3"
    "github.com/sparkwing-dev/sparks-core/docker"
    "github.com/sparkwing-dev/sparks-core/deploy"
)

func JobDeploy() {
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })
    sparkwing.RunStep(s3.Upload("my-bucket", "builds/", "dist/*.tar.gz"))
    deploy.Run(deploy.Config{
        AppName:   "myapp",
        Namespace: "prod",
        Images:    []string{"myapp"},
    })
    sparkwing.RunStep(slack.Notify("#deploys", "myapp deployed"))
}
\`\`\`

## Available plugins

| Plugin | Steps | Config |
|--------|-------|--------|
| **slack** | \`slack.Notify(channel, message)\` | \`SLACK_WEBHOOK_URL\` env var |
| **s3** | \`s3.Upload(bucket, prefix, patterns...)\`, \`s3.Download(bucket, key, dest)\` | AWS credentials from env/IAM |

## Writing a plugin

A plugin is a Go package that exports functions returning \`pipeline.Step\`:

\`\`\`go
package myplugin

import (
    "context"
    "github.com/sparkwing-dev/sparkwing/pkg/pipeline"
)

type myStep struct{ message string }

func MyStep(msg string) pipeline.Step { return &myStep{message: msg} }
func (s *myStep) Name() string { return "my-step" }
func (s *myStep) Run(ctx context.Context, rc *pipeline.RunContext) error {
    rc.Logger("hello: %s", s.message)
    return nil
}
\`\`\`
`,
  },
  {
    id: "hooks",
    title: "Hooks",
    content: `
## Lifecycle hooks

Hooks are shell scripts that run at specific points in the job lifecycle.

| Hook | When | Available in |
|------|------|-------------|
| \`pre-checkout\` | Before git pull | Agent only |
| \`post-checkout\` | After git pull | Agent + repo |
| \`pre-command\` | Before job runs | Agent + repo |
| \`post-command\` | After job (pass or fail) | Agent + repo |
| \`pre-exit\` | Always, during cleanup | Agent + repo |

## Repo hooks

Place scripts in \`.sparkwing/hooks/\`:

\`\`\`bash
# .sparkwing/hooks/post-command
#!/bin/bash
if [ "$SPARKWING_COMMAND_EXIT_STATUS" != "0" ]; then
  echo "Job failed for $SPARKWING_PIPELINE"
fi
\`\`\`

## Agent hooks

Place scripts in \`~/.config/sparkwing/hooks/\`. Agent hooks run before repo hooks.

## Environment variables

All hooks receive: \`SPARKWING_JOB_ID\`, \`SPARKWING_PIPELINE\`, \`SPARKWING_WORK_DIR\`, \`SPARKWING_BRANCH\`, \`SPARKWING_COMMIT\`, \`SPARKWING_COMMAND_EXIT_STATUS\` (post-command only).
`,
  },
  {
    id: "architecture",
    title: "Architecture",
    content: `
## How it works

\`\`\`
Controller (job queue + routing)
    |
    | assigns job to
    v
Listener (coordinator on your cluster)
    |
    | creates k8s Job pod
    v
Master Runner (isolated pod, executes job function)
    |
    | if Spawn/SpawnAll called
    v
Child Runners (via controller queue, any listener)
\`\`\`

**Controller** — central HTTP API. Queues jobs, routes to listeners, tracks results.

**Listener** — polls controller, manages concurrent job slots, creates master runner pods. Does NOT execute jobs itself.

**Master Runner** — isolated k8s pod. Downloads code from git cache, builds the .sparkwing/ binary, executes the registered job function.

**Git Cache** — centralized service. Clones repos once, serves cached tarballs over HTTP. SSH key lives only here.

**Local mode** — \`sparkwing pipeline run\` works without any cluster. Spawn creates subprocesses via an embedded mini-controller.
`,
  },
];

export default function LearnPage() {
  const [active, setActive] = useState("quickstart");
  const section = sections.find((s) => s.id === active);

  return (
    <div className="flex-1 flex overflow-hidden">
      {/* Sidebar */}
      <div className="w-56 border-r border-[var(--border)] bg-[var(--surface)] overflow-y-auto p-3">
        <div className="text-xs text-[var(--muted)] uppercase tracking-wider mb-3 px-2">
          Learn Sparkwing
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

// Simple markdown renderer for code blocks, headers, tables, and inline code
function MarkdownContent({ content }: { content: string }) {
  const lines = content.trim().split("\n");
  const elements: React.ReactNode[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Code blocks
    if (line.startsWith("```")) {
      const lang = line.slice(3);
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
        <InlineCode text={line} />
      </p>,
    );
    i++;
  }

  return <>{elements}</>;
}

function InlineCode({ text }: { text: string }) {
  const parts = text.split(/(`[^`]+`)/g);
  return (
    <>
      {parts.map((part, i) =>
        part.startsWith("`") && part.endsWith("`") ? (
          <code
            key={i}
            className="bg-[var(--background)] px-1.5 py-0.5 rounded text-xs font-mono text-cyan-400"
          >
            {part.slice(1, -1)}
          </code>
        : (
          <span key={i}>{part}</span>
       ,
      )}
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
                <InlineCode text={h} />
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, ri) => (
            <tr key={ri} className="border-b border-[var(--border)]">
              {row.map((cell, ci) => (
                <td key={ci} className="px-3 py-2">
                  <InlineCode text={cell} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
