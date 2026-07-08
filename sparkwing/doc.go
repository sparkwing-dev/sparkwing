// Package sparkwing is the author-facing SDK for declaring pipelines.
// Pipelines are Go structs with a Plan method that returns a typed
// DAG; the orchestrator (in a separate process) executes the DAG.
//
// # The two-layer model
//
// A [Pipeline] returns a [Plan]. A Plan is a DAG of [JobNode]s. Each
// JobNode in turn returns a [Work] -- its own inner DAG of [WorkStep]s.
// The outer layer is for cross-job structure (dependencies, retries,
// approvals, dynamic expansion); the inner layer is for ordering the
// concrete units of work inside one job. Most pipelines only use the
// inner layer when a job needs more than one step.
//
// # Registration
//
// Pipelines register themselves at package init via [Register], keyed
// by name. The CLI looks them up by name when the user runs
// `sparkwing run <name>`. The .sparkwing/jobs/*.go files in a
// consuming repo are the registration site; the .sparkwing/main.go
// blank-imports the jobs package and calls runner.Main.
//
// # Building Plans and Work
//
// Use [Job] to attach a job to a Plan. Use [JobApproval] for human
// gates and [JobSpawn] / [JobSpawnEach] for dynamic expansion at
// dispatch time. Inside a multi-step job, use [Step] on the supplied
// [Work] and order with [WorkStep.Needs] or [GroupSteps]. Cross-step
// data flow is via typed [Ref] values returned by [RefTo].
//
// # Modifier categories
//
// Plan-layer modifiers (set on a [JobNode]): Needs, Retry, Timeout,
// OnFailure, Cache, RunsOn, Inline, BeforeRun / AfterRun. Work-layer
// modifiers (set on a [WorkStep]): SkipIf, DryRun, [WorkStep.Risk],
// SafeWithoutDryRun. Risk labels are author-defined strings
// ("destructive", "prod", "rotates-key", ...) that gate execution
// behind the --sw-allow CLI flag.
//
// # Where to start
//
// Pipeline authors typically open a job file and look at three
// symbols: [Register] (how the pipeline gets named), [Job] (how a
// single-step job attaches to a Plan), and [Step] (how a multi-step
// Job builds its inner DAG). The other types in this package are
// either return values from these primitives or modifier methods on
// them.
package sparkwing
