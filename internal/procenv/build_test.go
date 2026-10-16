package procenv

import (
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

func TestCapturedEnvironmentAndClaimIdentity(t *testing.T) {
	gate := "/private/hook-index"
	rt := Runtime{RunID: "run", JobID: "job", Runner: RunnerInfo{Name: "granted", Type: "runner", Labels: []string{"linux", "pool=build"}}, Branch: "trunk", Commit: "abc", Repo: "repo", TriggerSource: "push", DryRun: true, GateIndex: gate}
	b := Base{Local: true, Captured: []string{"PATH=/bin", "CUSTOM=captured", "SPARKWING_CUSTOM=yes", "SPARKWING_RUNNER_NAME=forged", "CI=no", "SPARKWING_NODE_TOKEN=secret", "AWS_ACCESS_KEY_ID=secret", "MY_API_TOKEN=secret", "SPARKWING_NODE_URL=http://old"}, Ambient: []string{"CUSTOM=daemon"}}
	for _, kind := range []Kind{Plan, Attempt, Eval} {
		env, err := Build(kind, b, rt)
		if err != nil {
			t.Fatal(err)
		}
		got := envMap(env)
		for name, want := range map[string]string{"CUSTOM": "captured", "SPARKWING_CUSTOM": "yes", "SPARKWING_RUNNER_NAME": "granted", "CI": "true", "SPARKWING_RUN_ID": "run", "SPARKWING_BRANCH": "trunk", "SPARKWING_COMMIT": "abc", "SPARKWING_DRY_RUN": "true", "SPARKWING_GATE_INDEX": gate} {
			if got[name] != want {
				t.Errorf("kind %d %s=%q want %q", kind, name, got[name], want)
			}
		}
		for _, name := range []string{"SPARKWING_NODE_TOKEN", "AWS_ACCESS_KEY_ID", "MY_API_TOKEN", "SPARKWING_NODE_URL"} {
			if _, ok := got[name]; ok {
				t.Errorf("leaked %s", name)
			}
		}
		if kind == Plan && got["SPARKWING_JOB_ID"] != "" {
			t.Fatal("plan has job identity")
		}
		if kind != Plan && got["SPARKWING_JOB_ID"] != "job" {
			t.Fatal("attempt missing job identity")
		}
	}
	env, err := Build(Plan, Base{Local: true}, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SPARKWING_BRANCH", "SPARKWING_COMMIT", "SPARKWING_REPO", "SPARKWING_TRIGGER_SOURCE"} {
		if _, ok := envMap(env)[name]; ok {
			t.Errorf("invented %s", name)
		}
	}
}

func TestRemoteAllowListMasksBeforeExecution(t *testing.T) {
	masker := secrets.NewMasker()
	b := Base{Ambient: []string{"PATH=/bin", "HTTP_PROXY=http://alice:password@proxy", "CUSTOM=123456789012", "SHORT=12345678901", "OTHER=not-allowed", "MY_TOKEN=secret", "AWS_SECRET_ACCESS_KEY=secret", "SPARKWING_RUN_ID=forged"}, SubmitEnvAllow: []string{"CUSTOM", "SHORT", "MY_TOKEN", "AWS_SECRET_ACCESS_KEY", "SPARKWING_RUN_ID"}, Masker: masker}
	env, err := Build(Plan, b, Runtime{RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(env)
	if got["CUSTOM"] != "123456789012" || got["SHORT"] != "12345678901" || got["PATH"] != "/bin" {
		t.Fatal(got)
	}
	for _, name := range []string{"OTHER", "MY_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := got[name]; ok {
			t.Fatalf("leaked %s", name)
		}
	}
	for _, value := range []string{"123456789012", "password"} {
		if strings.Contains(masker.Mask(value), value) {
			t.Fatalf("unmasked %s", value)
		}
	}
	if masker.Mask("12345678901") != "12345678901" {
		t.Fatal("short public value masked")
	}
	b.Masker = nil
	if _, err := Build(Plan, b, Runtime{}); err == nil {
		t.Fatal("unmasked remote execution accepted")
	}
}

func TestJobEnvironmentPhases(t *testing.T) {
	input := []Assignment{{Name: "BODY", Value: "body"}, {Name: "MARKED", Value: "startup", Startup: true}, {Name: "NODE_OPTIONS", Value: "--trace-warnings"}, {Name: "GODEBUG", Value: "gctrace=1"}, {Name: "GOMAXPROCS", Value: "2"}}
	split, err := SplitJobEnv(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(split.Startup) != 4 || !reflect.DeepEqual(split.Body, input[:1]) {
		t.Fatalf("%+v", split)
	}
	rt := Runtime{Startup: split.Startup}
	b := Base{Local: true, Captured: []string{"PATH=/bin"}}
	plan, err := Build(Plan, b, rt)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envMap(plan)["MARKED"]; ok {
		t.Fatal("startup leaked to plan")
	}
	for _, kind := range []Kind{Attempt, Eval} {
		env, err := Build(kind, b, rt)
		if err != nil {
			t.Fatal(err)
		}
		if envMap(env)["MARKED"] != "startup" || envMap(env)["BODY"] != "" {
			t.Fatal(env)
		}
	}
	rt.AttemptEnv = []string{"PATH=/bin", "BODY=body", "SPARKWING_JOB_ID=job", "CI=true"}
	for _, kind := range []Kind{StepTool, Cleanup} {
		env, err := Build(kind, b, rt)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(env, rt.AttemptEnv) {
			t.Fatal(env)
		}
		env[0] = "changed"
		if rt.AttemptEnv[0] == "changed" {
			t.Fatal("aliased attempt env")
		}
	}
}

func TestValidateAssignments(t *testing.T) {
	for _, name := range []string{"", "A=B", "A\x00B", "SPARKWING_RUN_ID", "CI", "SPARKWING_NODE_CLAIM_X", "SPARKWING_NODE_TOKEN", "AWS_SECRET_ACCESS_KEY", "CUSTOM_TOKEN"} {
		err := Validate([]Assignment{{Name: name, Value: "never-echo-this-value"}})
		if err == nil {
			t.Errorf("accepted %q", name)
		} else if strings.Contains(err.Error(), "never-echo-this-value") {
			t.Fatal("secret in diagnostic")
		}
	}
	if err := Validate([]Assignment{{Name: "SPARKWING_CUSTOM", Value: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SplitJobEnv([]Assignment{{Name: "PATH", Value: "a\x00b"}}); err == nil {
		t.Fatal("NUL accepted")
	}
	for _, entry := range []string{"MALFORMED", "=bad", "A=bad\x00value"} {
		if _, err := Build(Plan, Base{Local: true, Captured: []string{entry}}, Runtime{}); err == nil {
			t.Errorf("accepted malformed input %q", entry)
		}
	}
}

func TestServiceAndInstallPolicies(t *testing.T) {
	b := Base{Local: true, Captured: []string{"PATH=/bin", "CUSTOM=yes", "SPARKWING_DEBUG=1", "SPARKWING_HOME=/sw", "XDG_CACHE_HOME=/cache", "GOCACHE=/go", "AWS_REGION=west", "KUBECONFIG=/kube", "SPARKWING_TOOLCHAIN_ACTIVE=1", "SPARKWING_NODE_URL=old"}}
	for _, kind := range []Kind{DaemonSpawn, Controller, Runner, Launcher, JobContainer, Install, RunHost, NodeHost, HostChild, CLI} {
		env, err := Build(kind, b, Runtime{RunID: "run"})
		if err != nil {
			t.Fatal(err)
		}
		got := envMap(env)
		if _, ok := got["SPARKWING_NODE_URL"]; ok {
			t.Fatal("removed protocol inherited")
		}
		if kind == RunHost || kind == NodeHost {
			if got["SPARKWING_RUN_ID"] != "run" || got["PATH"] != "" {
				t.Fatal(got)
			}
			continue
		}
		if got["PATH"] != "/bin" {
			t.Errorf("kind %d missing base", kind)
		}
		if kind != HostChild && kind != CLI && kind != Install && got["CUSTOM"] != "" {
			t.Errorf("kind %d inherits user input", kind)
		}
		if kind != CLI && got["SPARKWING_TOOLCHAIN_ACTIVE"] != "" {
			t.Errorf("kind %d inherits protocol", kind)
		}
	}
	if _, err := Build(Kind(255), b, Runtime{}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestLookupExactBeforePrefix(t *testing.T) {
	row, ok := Lookup("SPARKWING_REPO")
	if !ok || row.Class != RuntimeClass {
		t.Fatal(row)
	}
	row, ok = Lookup("SPARKWING_REPO_OLD")
	if !ok || row.Class != Protocol {
		t.Fatal(row)
	}
	row, ok = Lookup("UNREGISTERED")
	if ok || row.Owner != "user" || row.Class != User {
		t.Fatal(row)
	}
}

func TestInstallReceivesOnlyBase(t *testing.T) {
	for _, local := range []bool{true, false} {
		b := Base{Local: local, Captured: []string{"CUSTOM=", "PATH=/bin", "SPARKWING_DEBUG=1"}, Ambient: []string{"CUSTOM=", "PATH=/bin", "SPARKWING_DEBUG=1"}, SubmitEnvAllow: []string{"CUSTOM", "SPARKWING_DEBUG"}}
		env, err := Build(Install, b, Runtime{RunID: "run", Startup: []Assignment{{Name: "START", Value: "yes", Startup: true}}})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"CUSTOM": "", "PATH": "/bin"}
		if !reflect.DeepEqual(envMap(env), want) {
			t.Fatal(env)
		}
	}
}

func TestEveryBoundaryRejectsCredentialTransport(t *testing.T) {
	for kind := CLI; kind <= HostChild; kind++ {
		for _, name := range []string{"SPARKWING_NODE_TOKEN", "AWS_ACCESS_KEY_ID", "UNKNOWN_API_TOKEN", "LC_API_TOKEN", "SPARKWING_PROTOCOL_HANDLES"} {
			entry := name + "=sensitive"
			b := Base{Local: true, Captured: []string{entry}, Ambient: []string{entry}, SubmitEnvAllow: []string{name}}
			env, err := Build(kind, b, Runtime{AttemptEnv: []string{entry}})
			if kind == StepTool || kind == Cleanup {
				if err == nil {
					t.Fatalf("kind %d accepts %s", kind, name)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := envMap(env)[name]; ok {
				t.Fatalf("kind %d leaks %s", kind, name)
			}
		}
	}
	if _, err := Build(Plan, Base{Local: true}, Runtime{RunID: "run\x00bad"}); err == nil {
		t.Fatal("invalid runtime value accepted")
	}
}

func TestCABasePathsSurviveCredentialShapeFilter(t *testing.T) {
	b := Base{Ambient: []string{"SSL_CERT_FILE=/ca", "NODE_EXTRA_CA_CERTS=/node", "LC_API_TOKEN=secret"}, Masker: secrets.NewMasker()}
	env, err := Build(Plan, b, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)["SSL_CERT_FILE"] != "/ca" || envMap(env)["NODE_EXTRA_CA_CERTS"] != "/node" {
		t.Fatal(env)
	}
	if _, ok := envMap(env)["LC_API_TOKEN"]; ok {
		t.Fatal("prefix classification bypassed credential filter")
	}
	if err := Validate([]Assignment{{Name: "SSL_CERT_FILE", Value: "/ca"}}); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentNameCaseAndDuplicateResolution(t *testing.T) {
	_, known := Lookup("sparkwing_run_id")
	if known != (runtime.GOOS == "windows") {
		t.Fatal("lookup case policy differs from platform")
	}
	b := Base{Local: true, Captured: []string{"VALUE=first", "VALUE=last", "empty="}}
	env, err := Build(Plan, b, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)["VALUE"] != "last" {
		t.Fatal(env)
	}
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "VALUE=") {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate variable in child environment")
	}
	if _, ok := envMap(env)["empty"]; !ok {
		t.Fatal("explicit empty disappeared")
	}
}

func TestSnapshotAndRuntimeInputOwnership(t *testing.T) {
	captured := []string{"CUSTOM=before", "SPARKWING_RUN_ID=forged", "PATH=/bin"}
	before := append([]string(nil), captured...)
	env, err := Build(Attempt, Base{Local: true, Captured: captured, Ambient: []string{"CUSTOM=ambient"}}, Runtime{RunID: "claim-run", JobID: "claim-job"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured, before) {
		t.Fatal("Build changed captured snapshot")
	}
	for _, kind := range []Kind{StepTool, Cleanup} {
		got, err := Build(kind, Base{Local: true}, Runtime{RunID: "other-run", JobID: "other-job", AttemptEnv: env})
		if err != nil {
			t.Fatal(err)
		}
		if envMap(got)["SPARKWING_RUN_ID"] != "claim-run" || envMap(got)["SPARKWING_JOB_ID"] != "claim-job" {
			t.Fatal("tool identity replaced instead of inherited")
		}
	}
	captured[0] = "CUSTOM=changed"
	if envMap(env)["CUSTOM"] != "before" {
		t.Fatal("built environment aliased captured input")
	}
	if _, err := Build(Attempt, Base{Local: true}, Runtime{Startup: []Assignment{{Name: "SPARKWING_RUN_ID", Value: "spoofed", Startup: true}}}); err == nil {
		t.Fatal("startup spoofed claim identity")
	}
}

func TestProxyMaskingErrorsDoNotEchoValues(t *testing.T) {
	for _, local := range []bool{true, false} {
		b := Base{Local: local, Captured: []string{"HTTPS_PROXY=http://user:private-password@proxy"}, Ambient: []string{"HTTPS_PROXY=http://user:private-password@proxy"}}
		if _, err := Build(Plan, b, Runtime{}); err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatal("missing masker did not fail safely")
		}
		b.Masker = secrets.NewMasker()
		if _, err := Build(Plan, b, Runtime{}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.Masker.Mask("private-password"), "private-password") {
			t.Fatal("proxy password remained visible")
		}
	}
	b := Base{Local: true, Captured: []string{"HTTPS_PROXY=http://user:private-password@%"}, Masker: secrets.NewMasker()}
	if _, err := Build(Plan, b, Runtime{}); err == nil || strings.Contains(err.Error(), "private-password") {
		t.Fatal("malformed proxy diagnostic leaked input")
	}
}

func TestProxyCredentialsMaskedInConsumerSpellings(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"HTTPS_PROXY", "alice:private-password@proxy.example:3128"},
		{"http_proxy", "//alice:private-password@proxy.example:3128"},
		{"ALL_PROXY", "socks5://alice:private-password@proxy.example:1080"},
		{"GOPROXY", "https://public.example,https://alice:private-password@proxy.example|direct"},
		{"PIP_INDEX_URL", "https://alice:private-password@proxy.example/simple"},
		{"PIP_EXTRA_INDEX_URL", "https://public.example/simple https://alice:private-password@proxy.example/simple"},
		{"npm_config_registry", "https://alice:private-password@proxy.example"},
		{"HTTPS_PROXY", "http://alice:private%2Dpassword@proxy.example"},
		{"HTTPS_PROXY", "http://alice:private-password@proxy.example/simple/@scope?next=@other"},
		{"GOPROXY", "https://alice:private-password@proxy.example/@scope,https://bob:second-password@next.example|direct"},
		{"PIP_EXTRA_INDEX_URL", "https://public.example/@scope https://alice:private-password@proxy.example/simple/@scope"},
		{"npm_config_registry", "https://alice:private-password@proxy.example/@scope"},
	} {
		for _, local := range []bool{true, false} {
			t.Run(tc.name+"/"+tc.value+"/local="+strconv.FormatBool(local), func(t *testing.T) {
				entry := tc.name + "=" + tc.value
				b := Base{Local: local, Captured: []string{entry}, Ambient: []string{entry}, SubmitEnvAllow: []string{tc.name}, Masker: secrets.NewMasker()}
				env, err := Build(Plan, b, Runtime{})
				if err != nil {
					t.Fatal(err)
				}
				if envMap(env)[tc.name] != tc.value {
					t.Fatal("proxy setting was changed or dropped")
				}
				for _, value := range []string{tc.value, "alice", "private-password", "private%2Dpassword"} {
					if value == "private%2Dpassword" && !strings.Contains(tc.value, value) {
						continue
					}
					if strings.Contains(b.Masker.Mask(value), value) {
						t.Errorf("credential remained visible: %q", value)
					}
				}
				b.Masker = nil
				if _, err := Build(Plan, b, Runtime{}); err == nil {
					t.Fatal("proxy credentials accepted without masker")
				}
			})
		}
	}
}

func TestSubmitEnvPrefixPatterns(t *testing.T) {
	b := Base{Ambient: []string{"APP_COLOR=blue", "APP_=empty-suffix", "APP=bare", "APPLICATION=neighbor", "EXACT=literal", "EXACT_MORE=neighbor", "APP_API_TOKEN=secret", "SPARKWING_CUSTOM=public", "SPARKWING_RUN_ID=spoofed", "_CUSTOM=private"}, SubmitEnvAllow: []string{"APP_*", "EXACT", "SPARKWING_*", "_*"}, Masker: secrets.NewMasker()}
	for _, kind := range []Kind{Plan, Attempt, Eval, Install} {
		env, err := Build(kind, b, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		got := envMap(env)
		for name, want := range map[string]string{"APP_COLOR": "blue", "APP_": "empty-suffix", "EXACT": "literal", "SPARKWING_CUSTOM": "public", "_CUSTOM": "private"} {
			if got[name] != want {
				t.Errorf("kind %d missing prefix match %s", kind, name)
			}
		}
		for _, name := range []string{"APP", "APPLICATION", "EXACT_MORE", "APP_API_TOKEN", "SPARKWING_RUN_ID"} {
			if _, ok := got[name]; ok {
				t.Errorf("kind %d admitted %s", kind, name)
			}
		}
	}
}

func TestSubmitEnvMalformedPatternsFailBeforeMatching(t *testing.T) {
	for _, pattern := range []string{"", "*", "APP*", "APP_*_MORE", "APP_**", "APP?", "[APP]", " APP", "APP ", "APP\nOTHER", "APP\x00OTHER", "1APP", "APP=VALUE"} {
		for _, input := range [][]string{nil, {"APP_NAME=public"}} {
			_, err := Build(Plan, Base{Ambient: input, SubmitEnvAllow: []string{pattern}}, Runtime{})
			if err == nil {
				t.Errorf("accepted malformed pattern %q with input %v", pattern, input)
			}
		}
	}
}
