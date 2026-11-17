package sparkwing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestPublicMustVariable(t *testing.T) {
	ctx := withSecretResolver(t.Context(), secretResolverFunc(func(ctx context.Context, name string) (string, bool, error) {
		if name != "REGION" || !sdkserve.SecretIsVariable(ctx) {
			t.Fatalf("name=%s variable=%v", name, sdkserve.SecretIsVariable(ctx))
		}
		return "west", false, nil
	}))
	if got := MustVariable(ctx, "REGION"); got != "west" {
		t.Fatalf("value=%q", got)
	}
	t.Run("missing", func(t *testing.T) {
		defer func() {
			if err, ok := recover().(error); !ok || !strings.Contains(err.Error(), "MISSING") {
				t.Fatalf("panic=%v", err)
			}
		}()
		MustVariable(withSecretResolver(t.Context(), secretResolverFunc(func(context.Context, string) (string, bool, error) { return "", false, ErrSecretMissing })), "MISSING")
	})
}

func TestPublicCommandEnvironmentAndDecoders(t *testing.T) {
	var request nodeproto.ExecRequest
	output := `{"count":3}`
	ctx := sdkserve.WithHostExec(t.Context(), func(_ context.Context, req nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
		request = req
		return sdkserve.HostExecResult{Done: nodeproto.ExecDone{}, Stdout: []byte(output)}, nil
	})
	original := map[string]string{"BASE": "base", "OVERRIDE": "old", "": "ignored"}
	ctx = WithCommandEnv(ctx, original)
	original["BASE"] = "mutated"
	ctx = WithCommandEnv(ctx, map[string]string{"NESTED": "nested", "OVERRIDE": "context"})
	cmd := Exec(ctx, "program").EnvMap(map[string]string{"OVERRIDE": "explicit"})
	var decoded struct {
		Count int `json:"count"`
	}
	if err := cmd.JSON(&decoded); err != nil || decoded.Count != 3 {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
	for _, entry := range []string{"BASE=base", "NESTED=nested", "OVERRIDE=explicit"} {
		if !slices.Contains(request.Env, entry) {
			t.Fatalf("missing %s in %v", entry, request.Env)
		}
	}
	for _, entry := range request.Env {
		if strings.HasPrefix(entry, "=") {
			t.Fatalf("empty key=%s", entry)
		}
	}
	if WithCommandEnv(ctx, nil) != ctx {
		t.Fatal("empty env replaced context")
	}
	output = "invalid json"
	if err := cmd.JSON(&decoded); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	output = ""
	if err := cmd.MustBeEmpty("worktree clean"); err != nil {
		t.Fatal(err)
	}
	output = "dirty\n"
	if err := cmd.MustBeEmpty("worktree clean"); err == nil || !strings.Contains(err.Error(), "worktree clean") {
		t.Fatalf("error=%v", err)
	}
}

func TestPublicFreshInputsAndRouting(t *testing.T) {
	type inputs struct {
		Service string `flag:"service"`
		Count   int    `flag:"count"`
		Enabled bool   `flag:"enabled"`
	}
	options := []AwaitOption{WithFreshInputs(inputs{"api", 3, true}), WithFreshRepo("owner/repo"), WithFreshBranch("release")}
	aw := &stubAwaiter{runID: "child", data: []byte(`{"image":"built"}`)}
	ctx := context.WithValue(t.Context(), keyPipelineAwaiter, aw)
	out, err := RunAndAwait[awaitOut, inputs](ctx, "build", "image", options...)
	if err != nil || out.Image != "built" || aw.lastReq.Repo != "owner/repo" || aw.lastReq.Branch != "release" || !reflect.DeepEqual(aw.lastReq.Args, map[string]string{"service": "api", "count": "3", "enabled": "true"}) {
		t.Fatalf("out=%+v request=%+v error=%v", out, aw.lastReq, err)
	}
	t.Run("unsupported", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "WithFreshInputs") {
				t.Fatalf("panic=%v", r)
			}
		}()
		WithFreshInputs(struct {
			Bad chan int `flag:"bad"`
		}{})
	})
}

func TestPublicGitFactsAndRelay(t *testing.T) {
	helper := &publicHelpers{Services: sdkserve.ServicesFrom(t.Context())}
	ctx := sdkserve.WithServices(t.Context(), helper)
	helper.values = map[string]any{"git.CurrentSHA": strings.Repeat("a", 40), "git.CurrentBranch": "feature", "git.DefaultBranch": "main", "git.RemoteOriginURL": "git@github.com:owner/project.git", "git.IsDirty": true, "git.ChangedFiles": []string{"changed.go"}, "git.TagsAtHead": []string{"v1"}, "git.LatestTag": "v2"}
	g, err := NewGitFromTree(ctx, "/tree")
	if err != nil || g.SHA != strings.Repeat("a", 40) || g.Branch != "feature" || g.DefaultBranch != "main" || g.Repo != "owner/project" || g.WorkDir() != "/tree" || g.Name() != "project" || g.ShortSHA() != strings.Repeat("a", 12) {
		t.Fatalf("git=%+v error=%v", g, err)
	}
	if dirty, err := g.IsDirty(ctx); err != nil || !dirty {
		t.Fatalf("dirty=%v error=%v", dirty, err)
	}
	if files, err := g.ChangedFiles(ctx, "base"); err != nil || !slices.Equal(files, []string{"changed.go"}) {
		t.Fatalf("files=%v error=%v", files, err)
	}
	if tags, err := g.TagsAtHead(ctx); err != nil || !slices.Equal(tags, []string{"v1"}) {
		t.Fatalf("tags=%v error=%v", tags, err)
	}
	if tag, err := g.LatestTag(ctx, "v"); err != nil || tag != "v2" {
		t.Fatalf("tag=%v error=%v", tag, err)
	}
	if err := g.PushTag(ctx, "v3", "release"); err != nil {
		t.Fatal(err)
	}
	if last := helper.calls[len(helper.calls)-1]; last.Operation != "git.PushTag" || string(last.Args) != `["/tree","v3","release"]` {
		t.Fatalf("request=%+v", last)
	}
	var nilGit *Git
	if nilGit.ShortSHA() != "" || nilGit.Name() != "" {
		t.Fatal("nil git has facts")
	}
	for _, tc := range []struct{ slug, owner, repo string }{{" owner/project ", "owner", "project"}, {"owner", "", ""}, {"/repo", "", ""}, {"owner/", "", ""}, {"", "", ""}} {
		owner, repo := GithubOwnerRepo(tc.slug)
		if owner != tc.owner || repo != tc.repo {
			t.Fatalf("%q => %q/%q", tc.slug, owner, repo)
		}
	}
	helper.err = errors.New("host unavailable")
	if g, err := NewGitFromTree(ctx, "/tree"); g != nil || !errors.Is(err, helper.err) {
		t.Fatalf("git=%v error=%v", g, err)
	}
}

type publicHelpers struct {
	sdkserve.Services
	values map[string]any
	calls  []nodeproto.HelperRequest
	err    error
}

func (h *publicHelpers) Helper(_ context.Context, request nodeproto.HelperRequest, out any) error {
	h.calls = append(h.calls, request)
	if h.err != nil {
		return h.err
	}
	if out != nil {
		reflect.ValueOf(out).Elem().Set(reflect.ValueOf(h.values[request.Operation]))
	}
	return nil
}

func TestPublicOutcomesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		value        Outcome
		terminal, ok bool
	}{{Success, true, true}, {Memoized, true, true}, {Skipped, true, true}, {Failed, true, false}, {Cancelled, true, false}, {Superseded, true, false}, {Outcome("pending"), false, false}} {
		if tc.value.Terminal() != tc.terminal || tc.value.OK() != tc.ok {
			t.Errorf("outcome=%q terminal=%v ok=%v", tc.value, tc.value.Terminal(), tc.value.OK())
		}
	}
	cause := errors.New("reason")
	input := &InputError{Err: cause}
	verify := &VerifyError{Err: cause}
	exec := &ExecError{Cause: cause}
	if input.Error() != "reason" || !errors.Is(input, cause) || verify.Error() != "verify: reason" || !errors.Is(verify, cause) || !errors.Is(exec, cause) || !errors.Is(input.Unwrap(), cause) || !errors.Is(verify.Unwrap(), cause) || !errors.Is(exec.Unwrap(), cause) {
		t.Fatalf("input=%v verify=%v exec=%v", input, verify, exec)
	}
	for _, tc := range []struct {
		stage FailureStage
		want  string
	}{{StageAction, "action"}, {StageVerify, "verify"}} {
		if tc.stage.String() != tc.want {
			t.Fatalf("stage=%s", tc.stage)
		}
	}
}

func TestPublicGroupDeclarations(t *testing.T) {
	p := NewPlan()
	class := AdmissionBatch
	if p.AdmissionClass(class) != p {
		t.Fatal("plan chain lost")
	}
	producer := Job(p, "producer", func(context.Context) error { return nil })
	var generate GenerateFn = func(context.Context, *Plan) error { return nil }
	generator := producer.Generate(generate)
	group := GroupJobs(p, "members", Job(p, "a", func(context.Context) error { return nil }), Job(p, "b", func(context.Context) error { return nil }))
	limit := ConcurrencyLimit{Capacity: 3}
	concurrency := NewConcurrencyGroup("budget", limit)
	if concurrency.Name() != "budget" || concurrency.Limit() != limit {
		t.Fatalf("group=%v", concurrency)
	}
	key := KeyFromFile("go.sum")
	cache := Dir(".tools", key)
	memoOpts := []MemoOption{MemoTTL(time.Minute)}
	envOpts := []EnvOption{Startup()}
	retryOpts := []RetryOption{Backoff(time.Second)}
	skipOpts := []SkipOption{SkipBudget(time.Second)}
	var predicate SkipPredicate = func(context.Context) bool { return true }
	var before BeforeRunFn = func(context.Context) error { return nil }
	var after AfterRunFn = func(context.Context, error) {}
	var verify VerifyFn = func(context.Context) error { return nil }
	consume := []ConsumeOption{Into("input")}
	var dependency Dep = producer
	group.NeedsIfPresent(dependency).NeedsGenerated(generator).Memo(MemoInput{Kind: "const", Value: "v"}, memoOpts...).Cache(cache, GoModules()).Artifacts("out/*").Env("MODE", "test", envOpts...).Concurrency(concurrency, 2).Consumes(producer, consume...).Requires("linux").Inline().Optional().ContinueOnError().SkipIf(predicate, skipOpts...).BeforeRun(before).AfterRun(after).Verify(verify).Retry(2, retryOpts...)
	doc := planDocument(p)
	if doc.AdmissionClass == nil || *doc.AdmissionClass != "batch" {
		t.Fatalf("class=%v", doc.AdmissionClass)
	}
	for _, s := range doc.Jobs[1:] {
		if !slices.Equal(s.NeedsIfPresent, []string{"producer"}) || !slices.Equal(s.NeedsGenerated, []string{generator.ID()}) || s.Memo == nil || *s.Memo.TTLMS != 60000 || len(s.Caches) != 2 || s.Caches[0].Path != ".tools" || s.Caches[1].Resolver != "go-modules" || !slices.Equal(s.Artifacts, []string{"out/*"}) || len(s.Env) != 1 || s.Env["MODE"].Value != "test" || !s.Env["MODE"].Startup || len(s.Concurrency) != 1 || *s.Concurrency[0].Cost != 2 || len(s.Consumes) != 1 || s.Consumes[0].Into != "input" || !slices.Equal(s.Requires, []string{"linux"}) || !s.Inline || !s.Optional || !s.ContinueOnError || len(s.Closures.SkipIf) != 1 || len(s.Closures.BeforeRun) != 1 || len(s.Closures.AfterRun) != 1 || s.Closures.Verify == "" || s.Retry.Attempts != 2 {
			t.Fatalf("member=%+v", s)
		}
	}
	approval := p.Approval("approve", ApprovalConfig{OnTimeout: ApprovalTimeoutPolicy("fail")}).NeedsIfPresent(group).Optional().ContinueOnError()
	if approval.ID() != "approve" {
		t.Fatal(approval.ID())
	}
	s := jobDocument(p, approval.node)
	if !slices.Equal(s.NeedsIfPresent, []string{"a", "b"}) || !s.Optional || !s.ContinueOnError {
		t.Fatalf("approval=%+v", s)
	}
	n := Job(p, "single", func(context.Context) error { return nil }).NeedsIfPresent(producer).NeedsGenerated(generator).Inline().Optional().NoProgressTimeout(time.Second).Verify(verify)
	s = jobDocument(p, n)
	if !s.Inline || !s.Optional || *s.NoProgressTimeoutMS != 1000 || s.Closures.Verify == "" || len(s.NeedsGenerated) != 1 || len(s.NeedsIfPresent) != 1 {
		t.Fatalf("job=%+v", s)
	}
}

func TestPublicStepGroupDeclarations(t *testing.T) {
	w := NewWork()
	policy := CollectAll
	w.FailurePolicy(policy)
	first := Step(w, "first", func(context.Context) error { return nil })
	group := GroupSteps(w, "pair", Step(w, "a", func(context.Context) error { return nil }), Step(w, "b", func(context.Context) error { return nil }))
	var dependency WorkDep = first
	group.Needs(dependency).SkipIf(func(context.Context) bool { return true })
	for _, s := range group.Members() {
		if !slices.Equal(s.needs, []string{"first"}) || len(s.skipIf) != 1 || !s.skipIf[0](t.Context()) {
			t.Fatalf("step=%+v", s)
		}
	}
	cleanup := Step(w, "cleanup", func(context.Context) error { return nil }).ContinueOnError().Finally()
	if !cleanup.continueOnError || !cleanup.finally || w.failures != CollectAll {
		t.Fatalf("cleanup=%+v policy=%v", cleanup, w.failures)
	}
}

type publicMetadata struct{ Base }

func (publicMetadata) Plan(context.Context, *Plan, NoInputs, RunContext) error { return nil }
func (publicMetadata) Help() string                                            { return "Build the application" }
func (publicMetadata) ShortHelp() string                                       { return "Build" }
func (publicMetadata) Examples() []Example {
	return []Example{{Comment: "build", Command: "sparkwing run build"}}
}

func (publicMetadata) EnvVars() []EnvVarDoc {
	return []EnvVarDoc{{Name: "REGION", Description: "region", Default: "west"}}
}

func (publicMetadata) Secrets() any {
	return &struct {
		Token string `sw:"TOKEN,optional"`
	}{}
}

func TestPublicDescriptionProviders(t *testing.T) {
	isolateRegistry(t)
	p := publicMetadata{}
	var pipeline Pipeline[NoInputs] = p
	if err := pipeline.Plan(t.Context(), NewPlan(), NoInputs{}, RunContext{}); err != nil {
		t.Fatal(err)
	}
	var help HelpProvider = p
	var short ShortHelpProvider = p
	var examples ExampleProvider = p
	var env EnvVarDocer = p
	var secrets SecretsProvider = p
	if help.Help() != "Build the application" || short.ShortHelp() != "Build" || examples.Examples()[0].Command != "sparkwing run build" || env.EnvVars()[0].Default != "west" || secrets.Secrets() == nil {
		t.Fatal("providers lost metadata")
	}
	RegisterEntrypoint("Metadata", func() Pipeline[NoInputs] { return p })
	var operations sdkserve.Operations = sdkOperations{}
	if args := operations.Arguments(NewPlan()); len(args) != 0 {
		t.Fatal(args)
	}
	doc, err := operations.Describe(t.Context())
	if err != nil || len(doc.Pipelines) != 1 {
		t.Fatalf("document=%+v error=%v", doc, err)
	}
	got := doc.Pipelines[0]
	if got.Error != nil || got.Help != help.Help() || got.Short != short.ShortHelp() || len(got.Examples) != 1 || got.Examples[0].Command != "sparkwing run build" || len(got.EnvVars) != 1 || *got.EnvVars[0].Default != "west" || len(got.Secrets) != 1 || got.Secrets[0].Name != "TOKEN" || !got.Secrets[0].Optional {
		t.Fatalf("metadata=%+v", got)
	}
}

func TestPublicDebugAndWarn(t *testing.T) {
	logger := &recordingLogger{}
	var public Logger = logger
	public.Log("info", "direct")
	public.Emit(LogRecord{Level: "error", Msg: "emitted"})
	ctx := withTestLogger(t.Context(), logger)
	Debug(ctx, "detail %d", 3)
	Warn(ctx, "warning %s", "text")
	if !slices.Equal(logger.lines, []string{"info: direct", "error: emitted", "debug: detail 3", "warn: warning text"}) {
		t.Fatal(logger.lines)
	}
}

func TestPublicMainMissingProtocol(t *testing.T) {
	if os.Args[len(os.Args)-1] == "sparkwing-main-child" {
		Main()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestPublicMainMissingProtocol$", "--", "sparkwing-main-child")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "sw-node-protocol:") {
		t.Fatalf("output=%s error=%v", output, err)
	}
}

func TestPublicCallbackTypes(t *testing.T) {
	cause := errors.New("failure")
	var recovery FailureRecoveryFn = func(_ context.Context, f Failure) error {
		if f.Stage != StageVerify {
			t.Fatal(f)
		}
		return f.Err
	}
	if got := recovery(t.Context(), Failure{Stage: StageVerify, Err: cause}); !errors.Is(got, cause) {
		t.Fatal(got)
	}
	var config RetryConfig
	option := Backoff(time.Second)
	option(&config)
	if config.Backoff != time.Second || !config.backoffSet {
		t.Fatalf("retry=%+v", config)
	}
	referenceOptions := []RefOption{FromAnyRun()}
	ref := RefToLastRun[awaitOut]("build", "image", referenceOptions...)
	if !ref.AnyRun {
		t.Fatalf("reference=%+v", ref)
	}
	var work Workable = &jobFn{fn: func(context.Context) error { return nil }}
	w := NewWork()
	if _, err := work.Work(w); err != nil || len(w.steps) != 1 {
		t.Fatalf("work=%+v error=%v", w, err)
	}
}
