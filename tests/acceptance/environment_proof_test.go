package acceptance_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

func capturedEnvironment() error {
	base := procenv.Base{Local: true, Captured: []string{"SPARKWING_CUSTOM=captured", "EMPTY=", "SPARKWING_RUN_ID=spoofed", "SPARKWING_RUNNER_NAME=spoofed", "AWS_ACCESS_KEY_ID=secret"}, Ambient: []string{"SPARKWING_CUSTOM=daemon", "EMPTY=changed"}}
	rt := procenv.Runtime{RunID: "sealed-run", JobID: "build", Runner: procenv.RunnerInfo{Name: "granted-runner", Type: "runner"}, Branch: "trunk", Commit: "sealed-sha", Repo: "repo", TriggerSource: "schedule"}
	for _, kind := range []procenv.Kind{procenv.Plan, procenv.Attempt, procenv.Eval} {
		env, err := procenv.Build(kind, base, rt)
		if err != nil {
			return err
		}
		for name, want := range map[string]string{"SPARKWING_CUSTOM": "captured", "EMPTY": "", "SPARKWING_RUN_ID": "sealed-run", "SPARKWING_RUNNER_NAME": "granted-runner", "SPARKWING_BRANCH": "trunk", "SPARKWING_COMMIT": "sealed-sha", "SPARKWING_REPO": "repo", "SPARKWING_TRIGGER_SOURCE": "schedule", "CI": "true"} {
			if err := expectEnv(env, name, want, true); err != nil {
				return err
			}
		}
		if err := expectEnv(env, "AWS_ACCESS_KEY_ID", "", false); err != nil {
			return err
		}
	}
	child, err := procenv.Build(procenv.HostChild, base, rt)
	if err != nil {
		return err
	}
	return expectEnv(child, "SPARKWING_CUSTOM", "captured", true)
}

func remoteEnvironment() error {
	masker := secrets.NewMasker()
	base := procenv.Base{Ambient: []string{"PATH=/bin", "HTTP_PROXY=http://alice:proxy-password@proxy", "SSL_CERT_FILE=/ca", "ALLOWED=abcdefghijkl", "SHORT=abcdefghijk", "APP_COLOR=blue", "APP_API_TOKEN=credential", "UNKNOWN=hidden", "CUSTOM_API_TOKEN=credential", "SPARKWING_NODE_TOKEN=bearer"}, SubmitEnvAllow: []string{"ALLOWED", "SHORT", "APP_*", "CUSTOM_API_TOKEN", "SPARKWING_NODE_TOKEN"}, Masker: masker}
	for _, kind := range []procenv.Kind{procenv.Plan, procenv.Attempt, procenv.Eval, procenv.Install} {
		env, err := procenv.Build(kind, base, procenv.Runtime{RunID: "run"})
		if err != nil {
			return err
		}
		for name, want := range map[string]string{"PATH": "/bin", "SSL_CERT_FILE": "/ca", "ALLOWED": "abcdefghijkl", "SHORT": "abcdefghijk", "APP_COLOR": "blue"} {
			if err := expectEnv(env, name, want, true); err != nil {
				return err
			}
		}
		for _, name := range []string{"UNKNOWN", "APP_API_TOKEN", "CUSTOM_API_TOKEN", "SPARKWING_NODE_TOKEN"} {
			if err := expectEnv(env, name, "", false); err != nil {
				return err
			}
		}
		if kind == procenv.Install {
			if err := expectEnv(env, "SPARKWING_RUN_ID", "", false); err != nil {
				return err
			}
		}
	}
	for _, value := range []string{"abcdefghijkl", "proxy-password"} {
		if strings.Contains(masker.Mask(value), value) {
			return errors.New("remote environment value was not masked before execution")
		}
	}
	if masker.Mask("abcdefghijk") != "abcdefghijk" {
		return errors.New("eleven-character ordinary value was masked")
	}
	return nil
}

func environmentPhases() error {
	split, err := procenv.SplitJobEnv([]procenv.Assignment{{Name: "BODY", Value: "after-plan"}, {Name: "MARKED", Value: "startup", Startup: true}, {Name: "NODE_OPTIONS", Value: "--trace-warnings"}, {Name: "GODEBUG", Value: "gctrace=1"}, {Name: "GOMAXPROCS", Value: "2"}})
	if err != nil {
		return err
	}
	if len(split.Startup) != 4 || len(split.Body) != 1 || split.Body[0].Name != "BODY" {
		return errors.New("startup/body partition differs from declared phases")
	}
	base := procenv.Base{Local: true, Captured: []string{"PATH=/bin"}}
	rt := procenv.Runtime{RunID: "run", JobID: "job", Startup: split.Startup}
	plan, err := procenv.Build(procenv.Plan, base, rt)
	if err != nil {
		return err
	}
	for _, name := range []string{"BODY", "MARKED", "NODE_OPTIONS", "GODEBUG", "GOMAXPROCS"} {
		if err := expectEnv(plan, name, "", false); err != nil {
			return err
		}
	}
	attempt, err := procenv.Build(procenv.Attempt, base, rt)
	if err != nil {
		return err
	}
	if err := expectEnv(attempt, "MARKED", "startup", true); err != nil {
		return err
	}
	if err := expectEnv(attempt, "BODY", "", false); err != nil {
		return err
	}
	rt.Startup = append(rt.Startup, split.Body...)
	attempt, err = procenv.Build(procenv.Attempt, base, rt)
	if err != nil {
		return err
	}
	rt.AttemptEnv = attempt
	for _, kind := range []procenv.Kind{procenv.StepTool, procenv.Cleanup} {
		env, err := procenv.Build(kind, base, rt)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(env, rt.AttemptEnv) {
			return errors.New("tool or cleanup changed attempt body environment")
		}
	}
	return nil
}

func boundedEnvironmentText() error {
	masker := secrets.NewMasker()
	masker.Register("hidden-credential")
	text := "HEAD" + strings.Repeat("🙂hidden-credential", 5000) + "TAIL"
	bounded := limits.Error(masker.Mask(text))
	if len(bounded) > 8192 || !utf8.ValidString(bounded) || strings.Contains(bounded, "hidden-credential") || !strings.HasPrefix(bounded, "HEAD") || !strings.HasSuffix(bounded, "TAIL") || !strings.Contains(bounded, "elided") {
		return errors.New("masked error lost UTF-8, head/tail or byte bound")
	}
	annotation := strings.Repeat("x", 128<<10)
	if limits.Record(annotation) != annotation {
		return errors.New("boundary annotation changed")
	}
	if len(limits.Record(annotation+"x")) > 128<<10 {
		return errors.New("annotation exceeded byte bound")
	}
	raw := []byte(strings.Repeat("x", 64<<10))
	if result, err := limits.Failure(raw); err != nil || string(result) != string(raw) {
		return errors.New("boundary failure record changed")
	}
	if _, err := limits.Failure(append(raw, 'x')); err == nil {
		return errors.New("oversized structured failure was accepted")
	}
	if limits.Renew(30*time.Second) != 10*time.Second {
		return errors.New("renewal is not ttl/3")
	}
	if limits.CheckRenewal(10*time.Second, 30*time.Second) != nil || limits.CheckRenewal(15*time.Second, 30*time.Second) == nil {
		return errors.New("renewal strict boundary was not enforced")
	}
	return environmentDeadlines()
}

func environmentDeadlines() error {
	now := time.Unix(1000, 0)
	for _, parent := range []time.Time{now.Add(-time.Second), now, now.Add(5 * time.Second)} {
		if _, err := limits.Child(now, parent, time.Hour, 5*time.Second); !errors.Is(err, limits.ErrNoTime) {
			return errors.New("expired or margin-exhausted parent granted time")
		}
	}
	end, err := limits.Child(now, now.Add(time.Minute), time.Hour, 5*time.Second)
	if err != nil || !end.Equal(now.Add(55*time.Second)) {
		return errors.New("child did not reserve parent margin")
	}
	end, err = limits.Child(now, time.Time{}, 0, 0)
	if err != nil || !end.IsZero() {
		return errors.New("unbounded parent gained an execution limit")
	}
	owner := now.Add(time.Hour)
	attempt := now.Add(time.Minute)
	end, err = limits.JobDeadline(now, owner, attempt)
	if err != nil || !end.Equal(now.Add(11*time.Minute)) {
		return errors.New("job deadline did not add ten-minute slack")
	}
	owner = now.Add(2 * time.Minute)
	end, err = limits.JobDeadline(now, owner, attempt)
	if err != nil || !end.Equal(owner) {
		return errors.New("job deadline exceeded owner budget")
	}
	return nil
}

func expectEnv(env procenv.Env, name, want string, present bool) error {
	for entry := range env.Values() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == name {
			if !present || value != want {
				return fmt.Errorf("unexpected value or presence for %s", name)
			}
			return nil
		}
	}
	if present {
		return fmt.Errorf("missing environment variable %s", name)
	}
	return nil
}
