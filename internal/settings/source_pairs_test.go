package settings

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type sourceProbe func(Inputs) (any, Origin, error)

func probe[T any](fn func(Inputs) (T, Origin, error)) sourceProbe {
	return func(in Inputs) (any, Origin, error) {
		v, o, e := fn(in)
		return v, o, e
	}
}

func inputLayer(in *Inputs, s Source) *Values {
	switch s {
	case Flag:
		return &in.Flags
	case Env:
		return &in.Environment
	case sourceRun:
		return &in.Sealed
	case projectPipeline:
		return &in.Pipeline
	case projectDefaults:
		return &in.Defaults
	case code:
		return &in.Code
	case machine:
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
		{"profile", "Profile", []Source{Flag, sourceRun, projectPipeline}, "near", "far", probe(profile)},
		{"guards", "Guards", []Source{sourceRun, projectPipeline, projectDefaults}, projectconfig.Guards{When: []string{"where:local"}}, projectconfig.Guards{When: []string{"where:controller"}}, probe(Guards)},
		{"requires", "Requires", []Source{Flag, sourceRun, projectPipeline, projectDefaults, code}, []string{"near"}, []string{"far"}, probe(requires)},
		{"prefers", "Prefers", []Source{Flag, sourceRun, projectPipeline, projectDefaults, code}, []string{"near"}, []string{"far"}, probe(prefers)},
		{"workers", "Workers", []Source{Flag, sourceRun}, 2, 7, probe(workers)},
		{"output", "Output", []Source{Flag, Env, sourceRun}, "plain", "json", probe(output)},
		{"admission_class", "AdmissionClass", []Source{sourceRun, code}, "interactive", "batch", probe(admissionClass)},
		{"priority", "Priority", []Source{Flag, sourceRun, code}, 0, 9, probe(priority)},
		{"debug.pause_timeout", "PauseTimeout", []Source{sourceRun, machine}, time.Minute, 2 * time.Minute, probe(pauseTimeout)},
		{"runner", "RunnerSelection", []Source{Flag, sourceRun}, runspec.RunnerSelection{Kind: runspec.RunnerHere}, runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "worker"}, probe(runnerSelection)},
		{"start_at", "StartAt", []Source{Flag, sourceRun}, "near", "far", probe(startAt)},
		{"stop_at", "StopAt", []Source{Flag, sourceRun}, "near", "far", probe(stopAt)},
		{"only", "Only", []Source{Flag, sourceRun}, "near", "far", probe(only)},
		{"dry_run", "DryRun", []Source{Flag, sourceRun}, false, true, probe(dryRun)},
		{"no_memo", "NoMemo", []Source{Flag, sourceRun}, false, true, probe(noMemo)},
		{"allow_risk", "AllowRisk", []Source{Flag, sourceRun}, []string{}, []string{"destructive"}, probe(allowRisk)},
		{"job.no_progress_timeout", "NoProgressTimeout", []Source{sourceRun, code}, time.Duration(0), time.Minute, probe(noProgressTimeout)},
		{"job.retry_backoff", "RetryBackoff", []Source{sourceRun, code}, time.Duration(0), time.Second, probe(retryBackoff)},
		{"job.timeout", "JobTimeout", []Source{sourceRun, code}, time.Minute, 2 * time.Minute, probe(JobTimeout)},
		{"approval.wait", "ApprovalWait", []Source{sourceRun, code}, time.Duration(0), time.Minute, probe(ApprovalWait)},
		{"job.retry", "Retry", []Source{sourceRun, code}, 0, 4, probe(Retry)},
		{"job.memo_ttl", "MemoTTL", []Source{sourceRun, code}, time.Hour, 2 * time.Hour, probe(MemoTTL)},
		{"claim_wait", "ClaimWait", []Source{sourceRun, code}, time.Hour, 2 * time.Hour, probe(ClaimWait)},
		{"debug", "Debug", []Source{Env, sourceRun}, false, true, probe(debug)},
		{"publish", "Publish", []Source{Flag, sourceRun}, "near", "far", probe(publish)},
		{"cache_from", "CacheFrom", []Source{Flag, sourceRun, machine}, "near", "far", probe(cacheFrom)},
		{"run.submit_env_allow", "SubmitEnvAllow", []Source{sourceRun, machine}, []string{}, []string{"APP_*"}, probe(submitEnvAllow)},
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
