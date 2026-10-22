package settings

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func ptr[T any](v T) *T { return &v }

func present[T any](v T) runspec.Setting[T] { return declaration(v, runspec.SettingOrigin{}) }

func TestAdmissionClassInferenceAtPlanSeal(t *testing.T) {
	options, err := ResolveRequest(Inputs{Context: Context{Submitter: true, Local: true, NumCPU: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if options.AdmissionClass.Set {
		t.Fatal("intake promoted an inferred class to a request override")
	}
	for _, tc := range []struct {
		name    string
		origin  runspec.Origin
		code    *string
		request *string
		want    string
		layer   runspec.Layer
	}{
		{"scheduled", runspec.OriginSchedule, nil, nil, "batch", runspec.LayerDefault},
		{"local", runspec.OriginLocal, nil, nil, "normal", runspec.LayerDefault},
		{"code", runspec.OriginSchedule, ptr("interactive"), nil, "interactive", runspec.LayerCode},
		{"request", runspec.OriginSchedule, ptr("interactive"), ptr("critical"), "critical", runspec.LayerSealed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := options
			if tc.request != nil {
				opts.AdmissionClass = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: *tc.request}, Origin: runspec.SettingOrigin{Layer: runspec.LayerSealed, Key: "admission_class"}}
			}
			request, err := runspec.SealRequest(runspec.Request{Version: 1, Team: "team", Pipeline: "build", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: tc.origin, Options: opts, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}}, runspec.IntakeFacts{RunID: "run", Ceilings: runspec.Ceilings{AllowedAdmissionClasses: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{tc.want}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "operator.allowed_admission_classes"}}}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ResolvePlan(PlanInputs{Request: request, Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Plan: nodeproto.PlanDoc{AdmissionClass: tc.code}, Context: Context{Local: true}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Options.AdmissionClass.Value != tc.want || got.Options.AdmissionClass.Origin.Layer != tc.layer {
				t.Fatalf("admission class = %+v", got.Options.AdmissionClass)
			}
			if !reflect.DeepEqual(request.Request.Options, opts) {
				t.Fatal("plan resolution rewrote the request")
			}
		})
	}
}

func TestRequestOverridesSurvivePlan(t *testing.T) {
	request, err := ResolveRequest(Inputs{Context: Context{Submitter: true, Local: true, NumCPU: 8}, Flags: Values{Options: runspec.Options{Priority: present(0), Requires: present([]string{}), DryRun: present(false), StartAt: present("build/compile"), StopAt: present("test/verify"), Only: present("build*")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Priority.Set || request.Priority.Value != 0 || !request.Requires.Set || request.Requires.Value == nil || !request.DryRun.Set {
		t.Fatalf("lost presence: %+v", request)
	}
	if request.Resources.Set || request.ClaimWait.Set {
		t.Fatal("intake sealed checkout defaults")
	}
	declarations, err := ProjectCheckoutDeclarations([]byte("defaults:\n  requires: [linux]\n"), "sparkwing.yaml", "build")
	if err != nil {
		t.Fatal(err)
	}
	plan := nodeproto.PlanDoc{Priority: ptr(7), Jobs: []nodeproto.JobSpec{{ID: "test", Requires: []string{"go"}, TimeoutMS: ptr(int64(1000))}}}
	result, err := ResolvePlan(PlanInputs{Request: runspec.SealedRequest{Request: runspec.Request{Options: request}}, Declarations: declarations, Plan: plan, Context: Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Options.Priority.Value != 0 || result.Options.Priority.Origin.Layer != runspec.LayerFlag {
		t.Fatalf("priority: %+v", result.Options.Priority)
	}
	if result.Options.StartAt != request.StartAt || result.Options.StopAt != request.StopAt || result.Options.Only != request.Only {
		t.Fatal("plan resolution changed the intake step selections")
	}
	if !reflect.DeepEqual(result.Jobs["test"].Requires.Value, []string{"go"}) {
		t.Fatalf("requires: %+v", result.Jobs["test"].Requires)
	}
	if result.Jobs["test"].Timeout.Value != time.Second {
		t.Fatal("job timeout not resolved")
	}
	if request.Priority.Value != 0 || request.ClaimWait.Set {
		t.Fatal("resolution mutated intake")
	}
}

func TestPlanResolutionDoesNotAliasCapturedRequest(t *testing.T) {
	options, err := ResolveRequest(Inputs{Context: Context{Local: true, NumCPU: 2}, Flags: Values{Options: runspec.Options{AllowRisk: present([]string{"prod"})}}, Sealed: Values{Options: runspec.Options{Pause: present(runspec.PauseSelection{Before: present([]string{"job"})}), SubmitEnvAllow: present([]string{"APP_*"})}}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePlan(PlanInputs{Request: runspec.SealedRequest{Request: runspec.Request{Options: options}}, Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Context: Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	resolved.Options.AllowRisk.Value[0] = "changed"
	resolved.Options.Pause.Value.Before.Value[0] = "changed"
	resolved.Options.SubmitEnvAllow.Value[0] = "CHANGED_*"
	if options.AllowRisk.Value[0] != "prod" || options.Pause.Value.Before.Value[0] != "job" || options.SubmitEnvAllow.Value[0] != "APP_*" {
		t.Fatal("plan winners alias the immutable request")
	}
}

func TestNativeCompoundSettingsRetainParentAndLeafOrigins(t *testing.T) {
	parent := runspec.SettingOrigin{Layer: runspec.LayerFlag, File: "captured", Key: "resources", Line: 11}
	leaf := runspec.SettingOrigin{Layer: runspec.LayerEnv, File: "environment", Key: "memory", Line: 22}
	options := runspec.Options{
		Resources: declaration(runspec.Resources{Cores: present(0.5), MemoryBytes: declaration(int64(1024), leaf)}, parent),
		Pause:     declaration(runspec.PauseSelection{Before: present([]string{"job"})}, parent),
	}
	resolved, err := ResolveRequest(Inputs{Sealed: Values{Options: options}, Context: Context{Local: true, NumCPU: 2}})
	if err != nil {
		t.Fatal(err)
	}
	cores := resolved.Resources.Value.Cores.Origin
	pause := resolved.Pause.Value.Before.Origin
	if cores.Layer != parent.Layer || cores.File != parent.File || cores.Line != parent.Line || cores.Key != "resources.cores" || resolved.Resources.Value.MemoryBytes.Origin != leaf || pause.Layer != parent.Layer || pause.Line != parent.Line || pause.Key != "debug.pause.before" {
		t.Fatalf("compound provenance lost: resources=%+v pause=%+v", resolved.Resources, resolved.Pause)
	}
}

func TestPlanWithEmptySealedCeilingsUsesShippedOwnerBound(t *testing.T) {
	resolved, err := ResolvePlan(PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", TimeoutMS: ptr(int64(60000))}}}})
	if err != nil || resolved.Jobs["job"].Timeout.Value != time.Minute {
		t.Fatalf("empty sealed ceilings rejected a bounded job: %+v, %v", resolved, err)
	}
}

func TestDefaultsApplyWithoutPipelineAndExpansionUsesSameLayers(t *testing.T) {
	d, err := ProjectCheckoutDeclarations([]byte("defaults:\n  requires: [linux]\n  prefers: []\n  args: {mode: test}\n"), "sparkwing.yaml", "absent")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ResolvePlan(PlanInputs{Declarations: d, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "first", Requires: []string{"go"}}}}, Context: Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Jobs["first"].Requires.Value, []string{"linux", "go"}) {
		t.Fatalf("requires: %+v", result.Jobs)
	}
	generated, err := ResolveExpansion(ExpansionInputs{Spec: runspec.Spec{Declarations: d, Resolved: runspec.Resolved{Settings: result}}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "generated", Requires: []string{"docker"}}}}, Context: Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generated["generated"].Requires.Value, []string{"linux", "docker"}) {
		t.Fatalf("generated: %+v", generated)
	}
}

func TestSealedHostIgnoresAmbientPreferences(t *testing.T) {
	in := Inputs{Flags: Values{Options: runspec.Options{Output: present("invalid")}}, Environment: Values{Options: runspec.Options{Output: present("invalid")}}, Machine: Values{Options: runspec.Options{Workers: present(-1)}}, Sealed: Values{Options: runspec.Options{Output: present("plain"), Workers: present(3)}}}
	if value, _, err := Output(in); err != nil || value != "plain" {
		t.Fatalf("output %q: %v", value, err)
	}
	if value, _, err := Workers(in); err != nil || value != 3 {
		t.Fatalf("workers %d: %v", value, err)
	}
	if _, _, err := Output(Inputs{}); err == nil {
		t.Fatal("accepted host invented absent sealed setting")
	}
}

func TestAcceptedSettingsIgnoreOperatorAndLocalHost(t *testing.T) {
	in := Inputs{Context: Context{Local: true}, Sealed: Values{Options: runspec.Options{OwnerExecutionTimeout: present(12 * time.Hour), Priority: present(9), ClaimWait: present(2 * time.Hour), Resources: present(runspec.Resources{Cores: present(4.5)}), AdmissionClass: present("batch"), Checkout: sourceSetting(nodeproto.SourceSpec{FullHistory: ptr(false), Depth: ptr(1), Tags: ptr(false), Submodules: ptr(false), LFS: ptr(false)}, Origin{}), JobDefaults: runspec.JobExecutionSettings{Timeout: present(8 * time.Hour)}}}, Operator: OperatorInputs{Flags: OperatorValues{OwnerExecution: ptr(time.Hour), MaxPriority: ptr(1), MaxClaimWait: ptr(time.Minute), MaxCores: ptr(1.0), AllowedAdmissionClasses: []string{}, AllowedSources: []nodeproto.SourceSpec{}}}}
	if v, _, err := OwnerExecution(in); err != nil || v != 12*time.Hour {
		t.Fatalf("owner %v: %v", v, err)
	}
	if v, _, err := JobTimeout(in); err != nil || v != 8*time.Hour {
		t.Fatalf("timeout %v: %v", v, err)
	}
	if v, _, err := Priority(in); err != nil || v != 9 {
		t.Fatalf("priority %v: %v", v, err)
	}
	if v, _, err := ClaimWait(in); err != nil || v != 2*time.Hour {
		t.Fatalf("queue %v: %v", v, err)
	}
	if v, err := resourceSetting(in); err != nil || !v.Value.Cores.Set || v.Value.Cores.Value != 4.5 {
		t.Fatalf("resources %+v: %v", v, err)
	}
	if v, _, err := AdmissionClass(in); err != nil || v != "batch" {
		t.Fatalf("class %q: %v", v, err)
	}
	if _, err := checkoutSetting(in); err != nil {
		t.Fatal(err)
	}
}

func TestIntakePreservesExplicitRequestSettings(t *testing.T) {
	values := Values{Options: runspec.Options{ClaimWait: present(time.Hour), Resources: present(runspec.Resources{Cores: present(0.5)}), AdmissionClass: present("batch"), Checkout: sourceSetting(nodeproto.SourceSpec{Tags: ptr(true)}, Origin{}), JobDefaults: runspec.JobExecutionSettings{Retry: present(0), Timeout: present(time.Minute), MemoTTL: present(time.Hour), ApprovalWait: present(time.Duration(0)), NoProgressTimeout: present(time.Duration(0)), RetryBackoff: present(time.Duration(0))}}}
	options, err := ResolveRequest(Inputs{Context: Context{Submitter: true, NumCPU: 4, Local: true}, Sealed: values})
	if err != nil {
		t.Fatal(err)
	}
	if !options.JobDefaults.Retry.Set || options.JobDefaults.Retry.Value != 0 || !options.ClaimWait.Set || !options.Resources.Set || options.Resources.Value.Cores.Value != 0.5 || !options.Checkout.Set || options.Checkout.Value.Policy.Depth != nil || !options.JobDefaults.ApprovalWait.Set || !options.AdmissionClass.Set {
		t.Fatalf("lost request settings: %+v", options)
	}
}

func TestCeilingsRefuseWithoutClamping(t *testing.T) {
	for _, local := range []bool{false, true} {
		in := Inputs{Context: Context{Submitter: true, Local: local}, Code: Values{Options: runspec.Options{Resources: present(runspec.Resources{Cores: present(1.5)}), JobDefaults: runspec.JobExecutionSettings{Retry: present(11), MemoTTL: present(31 * 24 * time.Hour)}}}, Operator: OperatorInputs{Flags: OperatorValues{MaxCores: ptr(1.25)}}}
		if _, _, err := Retry(in); err == nil {
			t.Fatal("excess retry accepted")
		}
		if _, _, err := MemoTTL(in); err == nil {
			t.Fatal("excess memo TTL accepted")
		}
		_, err := resourceSetting(in)
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Value != "1.5" || refusal.Ceiling != "1.25" {
			t.Fatalf("missing resource evidence: %v", err)
		}
	}
}

func TestOperatorZeroWinsAndOriginsSurvive(t *testing.T) {
	in := OperatorInputs{Flags: OperatorValues{MaxRunsPerPrincipalHour: ptr(0)}, Team: OperatorValues{MaxRunsPerPrincipalHour: ptr(4)}}
	v, err := ControllerCeilings(in)
	if err != nil {
		t.Fatal(err)
	}
	if v.MaxRunsPerPrincipalHour == nil || *v.MaxRunsPerPrincipalHour != 0 || v.Origins["operator.max_runs_per_principal_hour"].Source != Flag {
		t.Fatalf("operator: %+v", v)
	}
	o := Origin{Source: Flag, File: "request", Key: "resources.cores", Line: 12}
	projected, err := ProjectCeilings(OperatorValues{MaxCores: ptr(0.5), Origins: map[string]Origin{"operator.max_cores": o}})
	if err != nil {
		t.Fatal(err)
	}
	if projected.Resources.Value.Cores.Value != 0.5 || projected.Resources.Value.Cores.Origin.Line != 12 {
		t.Fatalf("resource projection: %+v", projected)
	}
}

func TestCheckoutDeclarationsRejectForgedAuthority(t *testing.T) {
	d, err := ProjectCheckoutDeclarations([]byte("defaults:\n  args: {mode: test}\n  requires: []\n"), "sparkwing.yaml", "build")
	if err != nil {
		t.Fatal(err)
	}
	if d.Defaults.Requires.Value == nil || d.Defaults.Requires.Origin.Line != 3 {
		t.Fatalf("lost explicit empty/line: %+v", d)
	}
	entry := d.Defaults.Args.Value["mode"]
	entry.Origin.Layer = runspec.LayerFlag
	d.Defaults.Args.Value["mode"] = entry
	if err := ValidateCheckoutDeclarations(d); err == nil {
		t.Fatal("forged nested authority accepted")
	}
	if _, err := ProjectCheckoutDeclarations([]byte("profiles: {cloud: {token: hidden-secret}}"), "sparkwing.yaml", "build"); err == nil || strings.Contains(err.Error(), "hidden-secret") {
		t.Fatalf("project credential handling: %v", err)
	}
}

func TestIndependentDeadlineSettings(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true}, Operator: OperatorInputs{Flags: OperatorValues{OwnerExecution: ptr(12 * time.Hour)}}}
	if v, _, err := OwnerExecution(in); err != nil || v != 12*time.Hour {
		t.Fatalf("owner %v: %v", v, err)
	}
	if v, _, err := ClaimWait(in); err != nil || v != 24*time.Hour {
		t.Fatalf("queue %v: %v", v, err)
	}
	in.Code.JobDefaults.Timeout = present(13 * time.Hour)
	if _, _, err := JobTimeout(in); err == nil {
		t.Fatal("timeout above ceiling accepted")
	}
	in.Context.Local = true
	if v, _, err := OwnerExecution(in); err != nil || v != 0 {
		t.Fatalf("local deadline %v: %v", v, err)
	}
	if v, _, err := ApprovalWait(in); err != nil || v != 0 {
		t.Fatalf("approval %v: %v", v, err)
	}
}

func TestOutputDefaultsFollowTerminal(t *testing.T) {
	for _, tc := range []struct {
		terminal bool
		want     string
	}{{true, "pretty"}, {false, "json"}} {
		value, _, err := Output(Inputs{Context: Context{Submitter: true, Terminal: tc.terminal}})
		if err != nil || value != tc.want {
			t.Fatalf("terminal %v: %q, %v", tc.terminal, value, err)
		}
	}
}

func TestRetryBackoffRefusesValuesAboveExecutionBound(t *testing.T) {
	for _, duration := range []time.Duration{5*time.Minute + time.Nanosecond, 6 * time.Minute} {
		if _, _, err := RetryBackoff(Inputs{Code: Values{Options: runspec.Options{JobDefaults: runspec.JobExecutionSettings{RetryBackoff: present(duration)}}}, Context: Context{Submitter: true}}); err == nil {
			t.Errorf("accepted %v", duration)
		}
	}
	for _, duration := range []time.Duration{0, 5 * time.Minute} {
		if got, _, err := RetryBackoff(Inputs{Code: Values{Options: runspec.Options{JobDefaults: runspec.JobExecutionSettings{RetryBackoff: present(duration)}}}, Context: Context{Submitter: true}}); err != nil || got != duration {
			t.Fatalf("%v: %v %v", duration, got, err)
		}
	}
}
