package settings

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type sourceProbe func(Inputs) (any, Origin, error)

func probe[T any](fn func(Inputs) (T, Origin, error)) sourceProbe {
	return func(in Inputs) (any, Origin, error) {
		v, o, e := fn(in)
		if resources, ok := any(v).(runspec.Resources); ok {
			resources.Cores.Origin = runspec.SettingOrigin{}
			resources.MemoryBytes.Origin = runspec.SettingOrigin{}
			return resources, o, e
		}
		return v, o, e
	}
}

func inputLayer(in *Inputs, s Source) *Values {
	switch s {
	case Flag:
		return &in.Flags
	case Env:
		return &in.Environment
	case Run:
		return &in.Sealed
	case ProjectPipeline:
		return &in.Pipeline
	case ProjectDefaults:
		return &in.Defaults
	case Code:
		return &in.Code
	case Machine:
		return &in.Machine
	default:
		panic("bad fixture source")
	}
}

func TestRunSettingSourcePairs(t *testing.T) {
	rows := []struct {
		name, field   string
		sources       []Source
		first, second any
		resolve       sourceProbe
	}{
		{"profile", "Profile", []Source{Flag, Run, ProjectPipeline}, "near", "far", probe(Profile)},
		{"guards", "Guards", []Source{Run, ProjectPipeline, ProjectDefaults}, projectconfig.Guards{When: []string{"where:local"}}, projectconfig.Guards{When: []string{"where:controller"}}, probe(Guards)},
		{"requires", "Requires", []Source{Flag, Run, ProjectPipeline, ProjectDefaults, Code}, []string{"near"}, []string{"far"}, probe(Requires)},
		{"prefers", "Prefers", []Source{Flag, Run, ProjectPipeline, ProjectDefaults, Code}, []string{"near"}, []string{"far"}, probe(Prefers)},
		{"workers", "Workers", []Source{Flag, Run}, 2, 7, probe(Workers)},
		{"output", "Output", []Source{Flag, Env, Run}, "plain", "json", probe(Output)},
		{"admission_class", "AdmissionClass", []Source{Run, Code}, "interactive", "batch", probe(AdmissionClass)},
		{"priority", "Priority", []Source{Flag, Run, Code}, 0, 9, probe(Priority)},
		{"debug.pause_timeout", "PauseTimeout", []Source{Run, Machine}, time.Minute, 2 * time.Minute, probe(PauseTimeout)},
		{"schedule.catch_up", "CatchUp", []Source{Flag, Run, ProjectPipeline}, 2 * time.Hour, 3 * time.Hour, probe(CatchUp)},
		{"runner", "RunnerSelection", []Source{Flag, Run}, runspec.RunnerSelection{Kind: runspec.RunnerHere}, runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "worker"}, probe(RunnerSelection)},
		{"start_at", "StartAt", []Source{Flag, Run}, "near", "far", probe(StartAt)},
		{"stop_at", "StopAt", []Source{Flag, Run}, "near", "far", probe(StopAt)},
		{"only", "Only", []Source{Flag, Run}, "near", "far", probe(Only)},
		{"dry_run", "DryRun", []Source{Flag, Run}, false, true, probe(DryRun)},
		{"no_memo", "NoMemo", []Source{Flag, Run}, false, true, probe(NoMemo)},
		{"allow_risk", "AllowRisk", []Source{Flag, Run}, []string{}, []string{"destructive"}, probe(AllowRisk)},
		{"job.no_progress_timeout", "NoProgressTimeout", []Source{Run, Code}, time.Duration(0), time.Minute, probe(NoProgressTimeout)},
		{"job.retry_backoff", "RetryBackoff", []Source{Run, Code}, time.Duration(0), time.Second, probe(RetryBackoff)},
		{"job.timeout", "JobTimeout", []Source{Run, Code}, time.Minute, 2 * time.Minute, probe(JobTimeout)},
		{"approval.wait", "ApprovalWait", []Source{Run, Code}, time.Duration(0), time.Minute, probe(ApprovalWait)},
		{"job.retry", "Retry", []Source{Run, Code}, 0, 4, probe(Retry)},
		{"job.memo_ttl", "MemoTTL", []Source{Run, Code}, time.Hour, 2 * time.Hour, probe(MemoTTL)},
		{"claim_wait", "ClaimWait", []Source{Run, Code}, time.Hour, 2 * time.Hour, probe(ClaimWait)},
		{"idle_poll", "IdlePoll", []Source{Flag, Machine}, time.Second, 2 * time.Second, probe(IdlePoll)},
		{"debug", "Debug", []Source{Env, Run}, false, true, probe(Debug)},
		{"publish", "Publish", []Source{Flag, Run}, "near", "far", probe(Publish)},
		{"cache_from", "CacheFrom", []Source{Flag, Run, Machine}, "near", "far", probe(CacheFrom)},
		{"resources", "Resources", []Source{Run, Code}, runspec.Resources{Cores: present(0.5)}, runspec.Resources{Cores: present(2.5)}, probe(ResourceLimits)},
		{"source", "Checkout", []Source{Run, ProjectPipeline, Code}, nodeproto.SourceSpec{FullHistory: ptr(false), Depth: ptr(2), Tags: ptr(false), Submodules: ptr(false), LFS: ptr(false)}, nodeproto.SourceSpec{FullHistory: ptr(false), Depth: ptr(3), Tags: ptr(true), Submodules: ptr(true), LFS: ptr(true)}, probe(Checkout)},
		{"run.submit_env_allow", "SubmitEnvAllow", []Source{Run, Machine}, []string{}, []string{"APP_*"}, probe(SubmitEnvAllow)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for i, near := range row.sources {
				for _, far := range row.sources[i+1:] {
					in := Inputs{Context: Context{Submitter: true, Local: true, NumCPU: 8}}
					setFixture(inputLayer(&in, near), row.field, row.first)
					setFixture(inputLayer(&in, far), row.field, row.second)
					value, origin, err := row.resolve(in)
					if err != nil || !reflect.DeepEqual(value, row.first) || origin.Source != near {
						t.Fatalf("%s > %s: value=%#v origin=%+v err=%v", near, far, value, origin, err)
					}
					*inputLayer(&in, near) = Values{}
					value, origin, err = row.resolve(in)
					if err != nil || !reflect.DeepEqual(value, row.second) || origin.Source != far {
						t.Fatalf("fallback %s: value=%#v origin=%+v err=%v", far, value, origin, err)
					}
				}
			}
		})
	}
}

func setFixture(values *Values, field string, value any) {
	if field == "Checkout" {
		values.Checkout = sourceSetting(value.(nodeproto.SourceSpec), Origin{})
		return
	}
	names := map[string]string{"RunnerSelection": "Runner", "OwnerExecution": "OwnerExecutionTimeout", "JobTimeout": "Timeout"}
	if renamed, ok := names[field]; ok {
		field = renamed
	}
	root := reflect.ValueOf(values).Elem()
	switch field {
	case "Timeout", "NoProgressTimeout", "RetryBackoff", "ApprovalWait", "Retry", "MemoTTL":
		root = root.FieldByName("JobDefaults")
	}
	target := root.FieldByName(field)
	v := reflect.ValueOf(value)
	if target.Kind() == reflect.Struct && target.FieldByName("Set").IsValid() {
		target.FieldByName("Set").SetBool(true)
		target.FieldByName("Value").Set(v)
	} else if target.Kind() == reflect.Pointer {
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		target.Set(p)
	} else {
		target.Set(v)
	}
}

func TestArgsMergeAndScheduledOverrides(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true, ScheduleArgs: map[string]string{"mode": "schedule", "schedule": "yes"}, ScheduleHostArgs: map[string]string{"mode": "host"}}, Flags: Values{Args: map[string]string{"mode": "flag"}}, Pipeline: Values{Args: map[string]string{"mode": "pipeline", "project": "yes"}}, Defaults: Values{Args: map[string]string{"mode": "default", "default": "yes"}}, Code: Values{Args: map[string]string{"mode": "code", "code": "yes"}}}
	want := map[string]string{"mode": "flag", "schedule": "yes", "project": "yes", "default": "yes", "code": "yes"}
	got, origin, err := Args(in)
	if err != nil || !reflect.DeepEqual(got, want) || origin.Source != Flag {
		t.Fatalf("args: %#v %+v %v", got, origin, err)
	}
	in.Flags.Args = nil
	got, _, err = Args(in)
	if err != nil || got["mode"] != "host" {
		t.Fatalf("host override: %#v %v", got, err)
	}
	in.Context.ScheduleHostArgs = nil
	got, _, err = Args(in)
	if err != nil || got["mode"] != "schedule" {
		t.Fatalf("schedule override: %#v %v", got, err)
	}
}

func TestOperatorSettingSourcePairs(t *testing.T) {
	fields := []string{"RunRetentionDays", "MetricRetentionDays", "OwnerExecution", "MaxCores", "MaxMemoryBytes", "MaxPriority", "AllowedAdmissionClasses", "AllowedSources", "MaxClaimWait", "MaxConcurrentRunners", "MaxGlobalRunners", "RunnerAlarm", "MaxJobsPerRun", "MaxGlobalJobsPerRun", "MaxRunsPerPrincipalHour", "MaxGlobalRunsPerHour", "MinScheduleInterval", "ClaimsPerRunnerMinute", "HeartbeatsPerRunnerMinute", "RequestsPerTokenMinute", "RequestsPerMinuteAlarm", "MaxLogStreamsPerPrincipal", "MaxDownloadsPerPrincipal", "ShedQueueDepth", "EgressDailyAlarmBytes", "EnforceIdleClaimPoll"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			for near := 0; near < 4; near++ {
				for far := near + 1; far < 4; far++ {
					in := OperatorInputs{}
					layers := []*OperatorValues{&in.Flags, &in.Team, &in.Global, &in.Profile}
					expected := setOperatorFixture(layers[near], field, false)
					setOperatorFixture(layers[far], field, true)
					result, err := ControllerCeilings(in)
					if err != nil {
						t.Fatal(err)
					}
					actual := reflect.ValueOf(result).FieldByName(field).Interface()
					if !reflect.DeepEqual(actual, expected) {
						t.Fatalf("source %d > %d: %#v != %#v", near, far, actual, expected)
					}
				}
			}
		})
	}
}

func setOperatorFixture(values *OperatorValues, field string, far bool) any {
	target := reflect.ValueOf(values).Elem().FieldByName(field)
	n := int64(1)
	if far {
		n = 2
	}
	if target.Kind() == reflect.Slice {
		switch field {
		case "AllowedAdmissionClasses":
			v := []string{"normal"}
			if far {
				v = []string{"batch"}
			}
			target.Set(reflect.ValueOf(v))
		case "AllowedSources":
			target.Set(reflect.ValueOf([]nodeproto.SourceSpec{{FullHistory: ptr(false), Depth: ptr(int(n)), Tags: ptr(false), Submodules: ptr(false), LFS: ptr(false)}}))
		}
	} else {
		v := reflect.New(target.Type().Elem())
		switch v.Elem().Kind() {
		case reflect.Bool:
			v.Elem().SetBool(far)
		case reflect.Float64:
			v.Elem().SetFloat(float64(n) + 0.5)
		default:
			v.Elem().SetInt(n)
		}
		target.Set(v)
	}
	return target.Interface()
}
