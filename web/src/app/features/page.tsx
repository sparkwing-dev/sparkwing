"use client";

export default function FeaturesPage() {
  return (
    <div className="flex-1 overflow-y-auto">
      {/* Hero */}
      <div className="bg-gradient-to-b from-indigo-500/10 to-transparent px-8 py-16 text-center">
        <h1 className="text-4xl font-bold mb-4">
          CI/CD pipelines in <span className="text-indigo-400">Go</span>
        </h1>
        <p className="text-lg text-[var(--muted)] max-w-2xl mx-auto mb-8">
          Write your pipelines as real code, not YAML. Get compile-time errors, IDE autocomplete,
          and the full power of a real programming language for your CI/CD.
        </p>
        <div className="flex gap-4 justify-center">
          <code className="bg-[var(--surface)] border border-[var(--border)] px-4 py-2 rounded-lg text-sm font-mono">
            sparkwing cluster create --name dev
          </code>
        </div>
      </div>

      <div className="max-w-5xl mx-auto px-8 pb-16">
        {/* Why */}
        <section className="py-12">
          <h2 className="text-2xl font-bold mb-8 text-center">Why Sparkwing</h2>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
            {[
              {
                title: "Code, not config",
                desc: "Your pipelines are Go programs. Real imports, real functions, real error handling. No YAML templating nightmares.",
                color: "text-indigo-400",
              },
              {
                title: "One command setup",
                desc: "sparkwing cluster create gives you a full CI/CD stack in 2 minutes. Controller, listeners, dashboard, git cache — all local.",
                color: "text-green-400",
              },
              {
                title: "Content-addressed caching",
                desc: "Same code = same hash = skip. Tests that already passed? Instant. Builds that already ran? Cached. Zero wasted compute.",
                color: "text-cyan-400",
              },
              {
                title: "Structured pipelines",
                desc: "Jobs run sequentially. Parallel() runs jobs concurrently. Breakpoints let you pause and inspect. Timeouts keep things safe.",
                color: "text-violet-400",
              },
              {
                title: "GitHub native",
                desc: "Push to a branch → status check on your PR. Per-job reporting. Webhook-triggered. Branch enforcement for deploys.",
                color: "text-amber-400",
              },
              {
                title: "Secure by default",
                desc: "Deploy requirements, signed cache entries, rate limiting, audit logging, API auth. Trust verification before every deploy.",
                color: "text-red-400",
              },
            ].map((item) => (
              <div key={item.title} className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6">
                <h3 className={`font-bold mb-2 ${item.color}`}>{item.title}</h3>
                <p className="text-sm text-[var(--muted)]">{item.desc}</p>
              </div>
            ))}
          </div>
        </section>

        {/* Comparison */}
        <section className="py-12 border-t border-[var(--border)]">
          <h2 className="text-2xl font-bold mb-8 text-center">How Sparkwing Compares</h2>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--border)] text-[var(--muted)]">
                  <th className="px-4 py-3 text-left"></th>
                  <th className="px-4 py-3 text-left text-indigo-400">Sparkwing</th>
                  <th className="px-4 py-3 text-left">GitHub Actions</th>
                  <th className="px-4 py-3 text-left">Jenkins</th>
                  <th className="px-4 py-3 text-left">Buildkite</th>
                </tr>
              </thead>
              <tbody>
                {[
                  ["Pipeline language", "Go (compiled)", "YAML", "Groovy DSL", "YAML + bash"],
                  ["Compile-time errors", "Yes", "No", "No", "No"],
                  ["Self-hosted", "Yes (one command)", "No", "Yes (complex)", "Agents only"],
                  ["Local dev works", "Full stack local", "No", "No", "No"],
                  ["Job caching", "Content-addressed", "Manual", "Plugin", "Manual"],
                  ["Breakpoints", "Yes (.BreakBefore())", "No", "No", "No"],
                  ["Live log streaming", "SSE built-in", "Built-in", "Plugin", "Built-in"],
                  ["Per-job GitHub status", "Automatic", "Per-job", "Plugin", "Plugin"],
                  ["Pipeline as code", "Go imports", "YAML", "Jenkinsfile", "YAML + plugins"],
                  ["Setup time", "2 minutes", "N/A (SaaS)", "Weekend", "30 minutes"],
                ].map(([feature, ...values]) => (
                  <tr key={feature} className="border-b border-[var(--border)]">
                    <td className="px-4 py-2 font-medium">{feature}</td>
                    {values.map((v, i) => (
                      <td key={i} className={`px-4 py-2 ${i === 0 ? "text-indigo-400 font-medium" : "text-[var(--muted)]"}`}>
                        {v}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        {/* Code examples */}
        <section className="py-12 border-t border-[var(--border)]">
          <h2 className="text-2xl font-bold mb-8 text-center">Real Examples</h2>

          <div className="space-y-8">
            <div>
              <h3 className="font-bold mb-3">Simple build + deploy</h3>
              <pre className="bg-[#0d1117] border border-[var(--border)] rounded-lg p-4 text-sm font-mono overflow-x-auto">
{`package jobs

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
}`}
              </pre>
            </div>

            <div>
              <h3 className="font-bold mb-3">Parallel test shards + deploy</h3>
              <pre className="bg-[#0d1117] border border-[var(--border)] rounded-lg p-4 text-sm font-mono overflow-x-auto">
{`func JobMyapp() {
    // Run tests in parallel shards
    sparkwing.SpawnAll("test", []string{"0", "1", "2"},
        sparkwing.SpawnPipeline("myapp-test"),
        sparkwing.WithEnv(func(val string) map[string]string {
            return map[string]string{"SHARD": val}
        }),
    )

    // Lint in parallel via a named step
    sparkwing.RunStep(step.Shell("lint", "golangci-lint run"))

    // Build and push
    docker.BuildAndPush(docker.BuildConfig{
        Image:      "myapp",
        Dockerfile: "Dockerfile",
        Registries: registries,
    })

    // Gate deploy behind approval
    sparkwing.RunStep(step.RequireApproval("deploy-gate"))
    deploy.Run(deploy.Config{
        AppName:   "myapp",
        Namespace: "prod",
        Images:    []string{"myapp"},
    })
}`}
              </pre>
            </div>

            <div>
              <h3 className="font-bold mb-3">Dynamic monorepo detection</h3>
              <pre className="bg-[#0d1117] border border-[var(--border)] rounded-lg p-4 text-sm font-mono overflow-x-auto">
{`// Reads SPARKWING_CHANGED_FILES from the webhook
// Only builds apps whose files actually changed
func JobBuildChanged() {
    apps := changedApps()
    sparkwing.SpawnAll("build-changed", apps,
        sparkwing.SpawnPipeline("build-app"),
        sparkwing.WithEnv(func(app string) map[string]string {
            return map[string]string{"APP": app}
        }),
    )
}`}
              </pre>
            </div>
          </div>
        </section>

        {/* Features list */}
        <section className="py-12 border-t border-[var(--border)]">
          <h2 className="text-2xl font-bold mb-8 text-center">Every Feature</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
            {[
              "Pipeline framework (Pipeline/Job/Parallel/Post)",
              "Parallel job execution",
              "Job timeouts",
              "Pipeline breakpoints (.BreakBefore())",
              "Content-addressed job caching",
              "Real-time log streaming (SSE)",
              "Job cancellation",
              "Failed job retry",
              "GitHub webhook integration",
              "Per-job GitHub commit statuses",
              "Webhook signature verification",
              "Deploy branch enforcement",
              "Test verification before deploy",
              "Rate limiting on trigger API",
              "Audit logging (SQLite)",
              "HMAC-signed cache entries",
              "API bearer token auth",
              "Cron/scheduled builds",
              "Multi-repo support",
              "Artifact upload/download",
              "Build matrix (axis combinations)",
              "Concurrency controls (group limits)",
              "Shell/Script/Named steps",
              "Hooks lifecycle (5 points)",
              "Plugin system (Go modules)",
              "Git cache service (tarball serving)",
              "Git worktrees for local runs",
              "Local spawn (no cluster needed)",
              "Multi-cluster management",
              "Dynamic port allocation",
              "Per-workflow directories",
              "Metrics endpoint",
              "Flaky test detection",
              "Coverage reporting",
              "Web dashboard with pipeline viz",
              "Live log viewer",
              "Agent capacity monitoring",
              "Debug attach/env/continue",
            ].map((f) => (
              <div key={f} className="flex items-center gap-2 px-3 py-2 bg-[var(--surface)] rounded">
                <span className="text-green-400 shrink-0">&#10003;</span>
                <span>{f}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
