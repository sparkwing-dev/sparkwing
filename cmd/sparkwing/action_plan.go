package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type planPreviewDoc struct {
	Pipeline     string              `json:"pipeline"`
	ResolvedArgs map[string]string   `json:"resolved_args,omitempty"`
	StartAt      string              `json:"start_at,omitempty"`
	StopAt       string              `json:"stop_at,omitempty"`
	Jobs         []planPreviewJobDoc `json:"jobs"`
}

type planPreviewJobDoc struct {
	ID          string              `json:"id"`
	Deps        []string            `json:"deps,omitempty"`
	IsApproval  bool                `json:"is_approval,omitempty"`
	OnFailureOf string              `json:"on_failure_of,omitempty"`
	Decision    string              `json:"decision"`
	SkipReason  string              `json:"skip_reason,omitempty"`
	Work        *planPreviewWorkDoc `json:"work,omitempty"`
}

type planPreviewWorkDoc struct {
	Steps []planPreviewItemDoc `json:"steps,omitempty"`
}

type planPreviewItemDoc struct {
	ID         string   `json:"id"`
	Needs      []string `json:"needs,omitempty"`
	Decision   string   `json:"decision"`
	SkipReason string   `json:"skip_reason,omitempty"`
	SkipDetail string   `json:"skip_detail,omitempty"`

	Risks []string `json:"risks,omitempty"`
}

type pipelinePlanArgs struct {
	output      string
	pipeline    string
	startAt     string
	stopAt      string
	passthrough []string
}

func pipelinePlanOptions(inv clisurface.Invocation) pipelinePlanArgs {
	return pipelinePlanArgs{output: inv.Output(color.IsInteractiveStdout()), pipeline: inv.Value("pipeline"), startAt: inv.Value("start-at"), stopAt: inv.Value("stop-at"), passthrough: inv.Args}
}

func runPipelinePlan(inv clisurface.Invocation) error {
	parsed := pipelinePlanOptions(inv)
	format, err := resolveOutputFormat(parsed.output, cmdPipelinePlan.Path)
	if err != nil {
		return err
	}
	var doc planPreviewDoc
	err = withPipelineEntry(parsed.pipeline, func(ctx context.Context, entry pipelineInspection, _ string) error {
		planned, err := planPipelineEntry(ctx, entry, parsed.pipeline, parsed.passthrough)
		if err == nil {
			doc, err = previewPlan(planned, parsed.startAt, parsed.stopAt)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(doc)
	}
	printPin(parsed.pipeline)
	printPlanPreview(&doc)
	return nil
}

type pipelineInspection struct {
	planner nodehost.SDKPlanner
	input   nodehost.PlanningProcessInput
}

func withPipelineEntry(pipeline string, fn func(context.Context, pipelineInspection, string) error) error {
	directory, err := findSparkwingDir()
	if err != nil {
		return err
	}
	return withPipelineEntryAt(directory, pipeline, runFlags{}, fn)
}

func withPipelineEntryAt(directory, pipeline string, flags runFlags, fn func(context.Context, pipelineInspection, string) error) error {
	home, err := wingd.HomeDir("")
	if err != nil {
		return err
	}
	ctx, stop, raise := interruptContext()
	defer stop()
	defer func() {
		if ctx.Err() != nil {
			stop()
			raise()
		}
	}()
	prepared, err := prepareRunEnvironment(directory, pipeline, os.Environ(), flags)
	if err != nil {
		return err
	}
	flags.environment = prepared
	options, err := hostSourceOptions(home, prepared.captured, prepared.masker, true)
	if err != nil {
		return err
	}
	return withLocalPipelineEntry(ctx, home, directory, pipeline, flags, prepared.captured, options, fn)
}

func identifyCapturedEnvironment(home string, captured []string) (runspec.EnvSnapshot, error) {
	if home == "" {
		var err error
		if home, err = wingd.HomeDir(""); err != nil {
			return runspec.EnvSnapshot{}, err
		}
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	return snapshots.Identify(captured)
}

func withLocalPipelineEntry(ctx context.Context, home, directory, pipeline string, flags runFlags, captured []string, options source.Options, fn func(context.Context, pipelineInspection, string) error) (resultErr error) {
	prepared, err := prepareRunEnvironment(directory, pipeline, captured, flags)
	if err != nil {
		return err
	}
	captured, masker := prepared.captured, prepared.masker
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	selection, err := selectLocalRunSource(ctx, home, directory, pipeline, flags, captured, masker)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, selection.execution.Close()) }()
	options.SDK = ""
	options.Base, options.GitAccess = procenv.Base{Local: true, Captured: captured, Masker: masker}, source.GitAccessOwner
	install := options.InstallSDK
	options.InstallSDK = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
		if err := resolveSparks(ctx, filepath.Join(request.Directory, request.PipelineDir), flags.noUpdate); err != nil {
			return engine.SDKCommand{}, err
		}
		if install != nil {
			return install(ctx, selected, request)
		}
		return selected.InstallSDK(ctx, request)
	}
	spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: selection.git, Source: selection.source}})
	if err != nil {
		return err
	}
	entry, err := source.Acquire(ctx, spec, options)
	if entry != nil {
		defer func() { resultErr = errors.Join(resultErr, entry.Close()) }()
	}
	if err != nil {
		return err
	}
	masked := &installDiagnostics{out: os.Stderr, stream: masker.Stream()}
	if err := errors.Join(writeEngineNotices(masked, entry.Engine.Notices), masked.close()); err != nil {
		return err
	}
	environment, err := identifyCapturedEnvironment(home, captured)
	if err != nil {
		return err
	}
	processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base}}
	inspection := pipelineInspection{
		planner: nodehost.SDKPlanner{Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: masker, Log: func(ctx context.Context, frame nodeproto.FrameLog) error {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			_, err := fmt.Fprintln(os.Stderr, frame.Message)
			return err
		}},
		input: nodehost.PlanningProcessInput{
			Entry: entry, Entrypoint: selection.identity.Entrypoint, Environment: environment,
			Run: nodeproto.RunContext{RunID: "inspection", Pipeline: pipeline}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
		},
	}
	return fn(ctx, inspection, filepath.Dir(selection.execution.Directory))
}

func (p pipelineInspection) presentation(ctx context.Context) (runspec.PipelinePresentation, error) {
	prepared, err := p.planner.Inspect(ctx, p.input)
	if err != nil {
		return runspec.PipelinePresentation{}, err
	}
	public := prepared.Declarations.Presentation()
	for i, private := range prepared.Declarations.Declarations().Pipelines {
		if private.Name == p.input.Entrypoint {
			schema := public.Pipelines[i]
			if schema.Error != nil {
				return runspec.PipelinePresentation{}, schema.Error
			}
			if schema.Name != "" {
				schema.Name = p.planner.Masker.Mask(p.input.Run.Pipeline)
			}
			return schema, nil
		}
	}
	return runspec.PipelinePresentation{}, errors.New("pipeline presentation unavailable")
}

type planned struct {
	pipeline string
	args     map[string]string
	doc      nodeproto.PlanDoc
}

func planPipelineEntry(ctx context.Context, entry pipelineInspection, pipeline string, args []string) (planned, error) {
	out := planned{pipeline: pipeline}
	prepared, err := entry.planner.Inspect(ctx, entry.input)
	if err != nil {
		return out, planError(pipeline, err)
	}
	schema := prepared.Description
	schema.Name = entry.input.Run.Pipeline
	resolvedArgs, err := parsePipelineArgs(schema, args)
	if err != nil {
		return out, planError(pipeline, prepared.Declarations.Error(&apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}))
	}
	entry.input.Args = resolvedArgs
	prepared, err = entry.planner.Prepare(ctx, entry.input, nodehost.PlanningInput{})
	if err != nil {
		return out, planError(pipeline, err)
	}
	out.args = maps.Clone(prepared.Args)
	for _, name := range prepared.SecretArgs {
		if _, ok := out.args[name]; ok {
			out.args[name] = "***"
		}
	}
	raw, err := prepared.Functions.Plan(ctx)
	if err == nil {
		out.doc, err = prepared.Functions.Validate(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: entry.planner.Masker})
	}
	if err != nil {
		err = &maskedCLIError{err: planError(pipeline, err), masker: entry.planner.Masker}
	}
	return out, err
}

type planFailure struct {
	msg   string
	cause *apierr.Error
}

func (e *planFailure) Error() string { return e.msg }
func (e *planFailure) Unwrap() error { return e.cause }

func planError(pipeline string, err error) error {
	var perr *apierr.Error
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &perr):
		return fmt.Errorf("%s: %w", pipeline, err)
	case perr.Code == apierr.InvalidArgs:
		return &planFailure{fmt.Sprintf("%s: invalid arguments: %s", pipeline, perr.Message), perr}
	case perr.Code == apierr.PlanFailed:
		return &planFailure{fmt.Sprintf("%s: the pipeline's Plan failed: %s", pipeline, perr.Message), perr}
	}
	return fmt.Errorf("%s: %w", pipeline, err)
}

func isInvalidArgs(err error) bool {
	var perr *apierr.Error
	return errors.As(err, &perr) && perr.Code == apierr.InvalidArgs
}

func previewPlan(p planned, startAt, stopAt string) (planPreviewDoc, error) {
	doc := planPreviewDoc{Pipeline: p.pipeline, ResolvedArgs: p.args, StartAt: startAt, StopAt: stopAt}
	if err := checkStepRange(p.doc, startAt, stopAt); err != nil {
		return doc, err
	}
	for _, n := range p.doc.Jobs {
		pn := planPreviewJobDoc{ID: n.ID, Deps: n.Needs, IsApproval: n.Approval != nil, OnFailureOf: n.OnFailureOf, Decision: "would_run"}
		if len(n.Steps) > 0 && n.Approval == nil {
			pn.Work = &planPreviewWorkDoc{}
			ranged := rangeSkips(n.Steps, startAt, stopAt)
			skipped := 0
			for _, s := range n.Steps {
				it := planPreviewItemDoc{ID: s.ID, Needs: s.Needs, Decision: "would_run", Risks: s.Risks}
				if reason, ok := ranged[s.ID]; ok {
					it.Decision, it.SkipReason, it.SkipDetail = "would_skip", "range_skip", reason
				} else if len(s.Closures.SkipIf) > 0 {
					it.Decision, it.SkipReason, it.SkipDetail = "may_skip", "user_skipif", "SkipIf is evaluated at run time"
				}
				if it.Decision == "would_skip" {
					skipped++
				}
				pn.Work.Steps = append(pn.Work.Steps, it)
			}
			if skipped > 0 && skipped == len(n.Steps) {
				pn.Decision, pn.SkipReason = "would_skip", "all_steps_skipped"
			}
		}
		doc.Jobs = append(doc.Jobs, pn)
	}
	return doc, nil
}

func checkStepRange(doc nodeproto.PlanDoc, startAt, stopAt string) error {
	known := map[string]bool{}
	for _, n := range doc.Jobs {
		if len(n.Steps) > 0 {
			for _, s := range n.Steps {
				known[s.ID] = true
			}
		}
	}
	for _, bound := range [][2]string{{"--sw-start-at", startAt}, {"--sw-stop-at", stopAt}} {
		if bound[1] == "" || known[bound[1]] {
			continue
		}
		ids := slices.Sorted(maps.Keys(known))
		msg := fmt.Sprintf("%s %q references unknown step %q", bound[0], bound[1], bound[1])
		if suggestion := sparkwingruntime.SuggestClosest(bound[1], ids); suggestion != "" {
			msg += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		return fmt.Errorf("%s (available steps: %s)", msg, strings.Join(ids, ", "))
	}
	return nil
}

func rangeSkips(steps []nodeproto.StepSpec, startAt, stopAt string) map[string]string {
	parents := map[string][]string{}
	children := map[string][]string{}
	for _, s := range steps {
		parents[s.ID] = s.Needs
		for _, d := range s.Needs {
			children[d] = append(children[d], s.ID)
		}
	}
	_, hasStart := parents[startAt]
	_, hasStop := parents[stopAt]
	if !hasStart && !hasStop {
		return nil
	}
	reason := "upstream of --start-at=" + startAt
	switch {
	case startAt != "" && stopAt != "":
		reason = fmt.Sprintf("outside --start-at=%s --stop-at=%s range", startAt, stopAt)
	case stopAt != "":
		reason = "downstream of --stop-at=" + stopAt
	}
	skips := map[string]string{}
	for _, s := range steps {
		if (hasStart && !reaches(startAt, s.ID, children)) || (hasStop && !reaches(stopAt, s.ID, parents)) {
			skips[s.ID] = reason
		}
	}
	return skips
}

func reaches(from, to string, adj map[string][]string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		if !seen[n] {
			seen[n] = true
			stack = append(stack, adj[n]...)
		}
	}
	return false
}

func printPlanPreview(doc *planPreviewDoc) {
	if doc.Pipeline != "" {
		fmt.Printf("Plan: %s\n", doc.Pipeline)
	}
	if doc.StartAt != "" || doc.StopAt != "" {
		fmt.Printf("Range: --start-at=%s --stop-at=%s\n",
			orDashStr(doc.StartAt), orDashStr(doc.StopAt))
	}
	if len(doc.ResolvedArgs) > 0 {
		fmt.Println("Args:")
		for _, k := range slices.Sorted(maps.Keys(doc.ResolvedArgs)) {
			fmt.Printf("  %s=%s\n", k, doc.ResolvedArgs[k])
		}
	}
	if len(doc.Jobs) == 0 {
		fmt.Println("(no jobs)")
		return
	}
	for _, n := range doc.Jobs {
		printPlanPreviewJob(&n, "  ")
	}
}

func printPlanPreviewJob(n *planPreviewJobDoc, indent string) {
	tag := "Job"
	if n.IsApproval {
		tag = "Approval"
	}
	if n.OnFailureOf != "" {
		tag = "Recovery"
	}
	decision := n.Decision
	if n.SkipReason != "" {
		decision += " (" + n.SkipReason + ")"
	}
	if n.OnFailureOf != "" {
		decision += " [OnFailure: " + n.OnFailureOf + "]"
	}
	fmt.Printf("%s%s %q [%s]\n", indent, tag, n.ID, decision)
	if n.Work != nil {
		printPlanPreviewWork(n.Work, indent+"  ")
	}
}

func printPlanPreviewWork(w *planPreviewWorkDoc, indent string) {
	for _, s := range w.Steps {
		printPlanPreviewItem("Step", &s, indent)
	}
}

func printPlanPreviewItem(kind string, it *planPreviewItemDoc, indent string) {
	decision := it.Decision
	if it.SkipReason != "" {
		decision += " (" + it.SkipReason + ")"
	}
	if len(it.Risks) > 0 {
		decision += " risks=" + strings.Join(it.Risks, ",")
	}
	fmt.Printf("%s%s %q [%s]\n", indent, kind, it.ID, decision)
	if it.SkipDetail != "" {
		fmt.Printf("%s  reason: %s\n", indent, it.SkipDetail)
	}
	if len(it.Needs) > 0 {
		fmt.Printf("%s  needs: %s\n", indent, strings.Join(it.Needs, ", "))
	}
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
