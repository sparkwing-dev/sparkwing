package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestParentCheckoutPrecedesMachineLocation(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true, ParentCheckout: "/parent"}, Machine: Values{Options: runspec.Options{RepoLocation: present("/registered")}}}
	value, origin, err := RepoLocation(in)
	if err != nil || value != "/parent" || origin.Source != Run {
		t.Fatalf("%q %+v %v", value, origin, err)
	}
	in.Flags.RepoLocation = present("/submitted")
	value, _, err = RepoLocation(in)
	if err != nil || value != "/submitted" {
		t.Fatalf("%q %v", value, err)
	}
}

func TestStartupCompoundPrecedence(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true, UserHome: "/home/test", Cloud: true}, Flags: Values{Cache: &userconfig.CacheConfig{MaxEntries: pointer(0)}, RunnerPreferences: &userconfig.RunnerConfig{MaxConcurrent: pointer(3)}, Admission: &userconfig.AdmissionConfig{Mode: pointer("custom")}}, Machine: Values{Cache: &userconfig.CacheConfig{MaxBytes: pointer("1GiB")}, RunnerPreferences: &userconfig.RunnerConfig{Labels: []string{"gpu"}}, Admission: &userconfig.AdmissionConfig{Budget: pointer("2,4GiB")}}}
	cache, _, err := BinaryCache(in)
	if err != nil || *cache.MaxEntries != 0 || *cache.MaxBytes != "1GiB" {
		t.Fatal(cache, err)
	}
	runner, _, err := RunnerPreferences(in)
	if err != nil || *runner.MaxConcurrent != 3 || len(runner.Labels) != 1 {
		t.Fatal(runner, err)
	}
	runner.Labels[0] = "changed"
	if in.Machine.RunnerPreferences.Labels[0] != "gpu" {
		t.Fatal("runner aliases config")
	}
	admission, _, err := AdmissionPolicy(in)
	if err != nil || *admission.Mode != "custom" || *admission.Budget != "2,4GiB" {
		t.Fatal(admission, err)
	}
	retention, _, err := Retention(in)
	if err != nil || *retention.RunDays != 90 {
		t.Fatal(retention, err)
	}
	in.Operator.Team.RunRetentionDays = pointer(0)
	in.Operator.Global.MetricRetentionDays = pointer(12)
	retention, _, err = Retention(in)
	if err != nil || *retention.RunDays != 0 || *retention.MetricDays != 12 {
		t.Fatal(retention, err)
	}
}

func TestSecretsKeyKeepsPrevious(t *testing.T) {
	in := Inputs{Context: Context{UserHome: "/home/test"}, Environment: Values{SecretsKey: &KeyInput{Current: pointer("private-current"), File: pointer("/unused"), Previous: pointer("private-previous")}}}
	keys, origin, err := SecretsKey(in)
	if err != nil || origin.Source != Env || keys.File != nil || *keys.Current != "private-current" || *keys.Previous != "private-previous" {
		t.Fatal("key selection failed", err)
	}
	in.Environment.SecretsKey.Current = pointer("")
	_, _, err = SecretsKey(in)
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("invalid key did not fail safely")
	}
	in.Environment.SecretsKey = nil
	keys, _, err = SecretsKey(in)
	if err != nil || *keys.File != "/home/test/.config/sparkwing/secrets.key" {
		t.Fatal(keys, err)
	}
}

func TestConnectionAndCheckoutFacts(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true, ParentCheckout: "/parent", RegisteredCheckouts: []string{"/registered"}, FallbackPaths: []string{"/fallback"}}}
	path, _, err := RepoLocation(in)
	if err != nil || path != "/parent" {
		t.Fatal(path, err)
	}
	in.Flags.RepoLocation = present("/submitted")
	path, _, err = RepoLocation(in)
	if err != nil || path != "/submitted" {
		t.Fatal(path, err)
	}
	in.Context.Submitter = false
	in.Sealed.RepoLocation = present("/sealed")
	path, _, err = RepoLocation(in)
	if err != nil || path != "/sealed" {
		t.Fatal(path, err)
	}
	in.Flags.Connection = &userconfig.Connection{URL: "https://controller.example", Token: pointer("private-token")}
	connection, _, err := ControllerConnection(in)
	if err != nil || connection.Token == nil || *connection.Token != "private-token" {
		t.Fatal("connection lost token", err)
	}
	*connection.Token = "changed"
	if *in.Flags.Connection.Token != "private-token" {
		t.Fatal("token aliases config")
	}
	in.Flags.Connection.URL = "https://user:private-token@controller.example"
	_, _, err = ControllerConnection(in)
	if err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("unsafe connection allowed or echoed")
	}
}

func TestPausePerFieldAndRunnerRenewals(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true}, Flags: Values{Options: runspec.Options{Pause: present(runspec.PauseSelection{Before: present([]string{}), OnFailure: present(false)})}}, Sealed: Values{Options: runspec.Options{Pause: present(runspec.PauseSelection{Before: present([]string{"old"}), After: present([]string{"after"}), OnFailure: present(true)})}}}
	pause, _, err := PauseTargets(in)
	if err != nil || len(pause.Before.Value) != 0 || len(pause.After.Value) != 1 || pause.OnFailure.Value {
		t.Fatal(pause, err)
	}
	in.Flags.RunnerPreferences = &userconfig.RunnerConfig{Lease: pointer("1m"), Heartbeat: pointer("30s")}
	if _, _, err := RunnerPreferences(in); err == nil {
		t.Fatal("two renewals not strictly before expiry")
	}
	in.Flags.Cache = &userconfig.CacheConfig{MaxEntries: pointer(-1)}
	if _, _, err := BinaryCache(in); err == nil {
		t.Fatal("negative cache entries accepted")
	}
}

func TestStartupSourcePairs(t *testing.T) {
	tests := []struct {
		key string
		set func(*Values, string)
		get func(Inputs) (string, Origin, error)
	}{
		{"config_path", func(v *Values, s string) { v.ConfigPath = pointer(s) }, ConfigPath},
		{"home", func(v *Values, s string) { v.Home = pointer(s) }, Home},
		{"toolchain", func(v *Values, s string) { v.Toolchain = pointer(s) }, Toolchain},
		{"machine.box_id", func(v *Values, s string) { v.BoxID = pointer(s) }, BoxID},
		{"secrets_key", func(v *Values, s string) { v.SecretsKey = &KeyInput{Current: pointer(s)} }, func(in Inputs) (string, Origin, error) {
			v, o, e := SecretsKey(in)
			if e != nil {
				return "", o, e
			}
			return *v.Current, o, nil
		}},

		{"admission", func(v *Values, s string) { v.Admission = &userconfig.AdmissionConfig{Mode: pointer(s)} }, func(in Inputs) (string, Origin, error) {
			v, o, e := AdmissionPolicy(in)
			if e != nil {
				return "", o, e
			}
			return *v.Mode, o, nil
		}},
		{"cache", func(v *Values, s string) { v.Cache = &userconfig.CacheConfig{MaxBytes: pointer(s)} }, func(in Inputs) (string, Origin, error) {
			v, o, e := BinaryCache(in)
			if e != nil {
				return "", o, e
			}
			return *v.MaxBytes, o, nil
		}},
		{"machine.runner", func(v *Values, s string) { v.RunnerPreferences = &userconfig.RunnerConfig{HolderPrefix: pointer(s)} }, func(in Inputs) (string, Origin, error) {
			v, o, e := RunnerPreferences(in)
			if e != nil {
				return "", o, e
			}
			return *v.HolderPrefix, o, nil
		}},
		{"connection", func(v *Values, s string) { v.Connection = &userconfig.Connection{URL: s} }, func(in Inputs) (string, Origin, error) { v, o, e := ControllerConnection(in); return v.URL, o, e }},
		{"repo_location", func(v *Values, s string) { v.RepoLocation = present(s) }, RepoLocation},
		{"debug.pause", func(v *Values, s string) {
			v.Pause = present(runspec.PauseSelection{Before: present([]string{s})})
		}, func(in Inputs) (string, Origin, error) {
			v, o, e := PauseTargets(in)
			if e != nil {
				return "", o, e
			}
			return v.Before.Value[0], o, nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			order := ruleFor(tc.key).Sources
			high, low := "higher", "lower"
			switch tc.key {
			case "toolchain":
				high, low = "auto", "local"
			case "admission":
				high, low = "classic", "off"
			case "cache":
				high, low = "10", "20"
			case "connection":
				high, low = "https://higher.example", "https://lower.example"
			case "repo_location":
				high, low = "/higher", "/lower"
			}
			for i, closer := range order {
				if closer == Default {
					continue
				}
				for _, farther := range order[i+1:] {
					t.Run(string(closer)+"_over_"+string(farther), func(t *testing.T) {
						in := Inputs{Context: Context{Submitter: true, UserHome: "/home/test"}}
						values := func(source Source) *Values {
							switch source {
							case Env:
								return &in.Environment
							case Flag:
								return &in.Flags
							case Run:
								return &in.Sealed
							case Machine:
								return &in.Machine
							default:
								t.Fatalf("unhandled source %s", source)
								return nil
							}
						}
						tc.set(values(closer), high)
						if farther != Default {
							tc.set(values(farther), low)
						}
						got, origin, err := tc.get(in)
						if err != nil || got != high || origin.Source != closer {
							t.Fatalf("got %q from %s: %v", got, origin.Source, err)
						}
					})
				}
			}
		})
	}
}

func TestRetentionSourcePairs(t *testing.T) {
	order := OperatorPrecedence()
	for i, closer := range order {
		if closer == Default {
			continue
		}
		for _, farther := range order[i+1:] {
			t.Run(string(closer)+"_over_"+string(farther), func(t *testing.T) {
				in := Inputs{Context: Context{Cloud: true}}
				values := func(s Source) *OperatorValues {
					switch s {
					case Flag:
						return &in.Operator.Flags
					case TeamDB:
						return &in.Operator.Team
					case GlobalDB:
						return &in.Operator.Global
					case LimitsProfile:
						return &in.Operator.Profile
					default:
						t.Fatal(s)
						return nil
					}
				}
				values(closer).RunRetentionDays = pointer(0)
				values(closer).MetricRetentionDays = pointer(7)
				if farther != Default {
					values(farther).RunRetentionDays = pointer(5)
					values(farther).MetricRetentionDays = pointer(8)
				}
				value, origin, err := Retention(in)
				if err != nil || *value.RunDays != 0 || *value.MetricDays != 7 || origin.Source != closer {
					t.Fatal(value, origin, err)
				}
			})
		}
	}
}

func TestCompoundErrorsRetainLeafOrigin(t *testing.T) {
	in := Inputs{Flags: Values{Cache: &userconfig.CacheConfig{MaxEntries: pointer(3)}}, Machine: Values{Cache: &userconfig.CacheConfig{MaxBytes: pointer("invalid-private-value")}, Origins: map[string]Origin{"cache": {Source: Machine, File: "config.yaml", Key: "cache", Line: 12}}}}
	_, _, err := BinaryCache(in)
	var settingErr *Error
	if !errors.As(err, &settingErr) || settingErr.From.Source != Machine || settingErr.From.Line != 12 || strings.Contains(err.Error(), "invalid-private-value") {
		t.Fatalf("lost leaf origin or exposed value: %v", err)
	}
}

func TestPausePreservesEachOrigin(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true}, Flags: Values{Options: runspec.Options{Pause: present(runspec.PauseSelection{Before: present([]string{"before"})})}}, Sealed: Values{Options: runspec.Options{Pause: present(runspec.PauseSelection{OnFailure: present(false)})}}}
	in.Sealed.Pause.Value.OnFailure.Origin = runspec.SettingOrigin{Layer: runspec.LayerEnv, File: "captured", Key: "debug.pause.on_failure", Line: 5}
	setting, err := pauseSetting(in)
	origins := setting.Value
	if err != nil || fromOrigin(origins.Before.Origin).Source != Flag || fromOrigin(origins.After.Origin).Source != Default || fromOrigin(origins.OnFailure.Origin).Source != Env || origins.OnFailure.Origin.Line != 5 {
		t.Fatalf("origins lost: %+v %v", origins, err)
	}
}

func TestRetentionFlagErrorKeepsOrigin(t *testing.T) {
	in := Inputs{Flags: Values{Retention: &RetentionValues{RunDays: pointer(-1)}, Origins: map[string]Origin{"retention": {Source: Flag, File: "flags", Key: "run-retention", Line: 3}}}}
	_, _, err := Retention(in)
	var field *Error
	if !errors.As(err, &field) || field.From.File != "flags" || field.From.Line != 3 {
		t.Fatalf("flag origin lost: %v", err)
	}
}

func TestAdmissionNestedFieldsAndMapKeysMerge(t *testing.T) {
	in := Inputs{Flags: Values{Admission: &userconfig.AdmissionConfig{Custom: &userconfig.AdmissionCustom{ClassWeight: map[string]int{"batch": 2}, InteractiveBurst: &userconfig.AdmissionBurst{Cores: pointer(2.0)}}}}, Machine: Values{Admission: &userconfig.AdmissionConfig{Custom: &userconfig.AdmissionCustom{ClassWeight: map[string]int{"normal": 3, "batch": 1}, AgingEvery: pointer(uint64(0)), InteractiveBurst: &userconfig.AdmissionBurst{MinSamples: pointer(5)}}}}}
	value, _, err := AdmissionPolicy(in)
	if err != nil || value.Custom.ClassWeight["normal"] != 3 || value.Custom.ClassWeight["batch"] != 2 || *value.Custom.InteractiveBurst.MinSamples != 5 || *value.Custom.InteractiveBurst.Cores != 2 {
		t.Fatal(value, err)
	}
	value.Custom.ClassWeight["normal"] = 10
	*value.Custom.InteractiveBurst.MinSamples = 10
	if in.Machine.Admission.Custom.ClassWeight["normal"] != 3 || *in.Machine.Admission.Custom.InteractiveBurst.MinSamples != 5 {
		t.Fatal("nested settings alias input")
	}
	in.Flags.Admission.Custom.ClassWeight = map[string]int{}
	value, _, err = AdmissionPolicy(in)
	if err != nil || len(value.Custom.ClassWeight) != 0 {
		t.Fatal("explicit empty map did not clear", err)
	}
}
