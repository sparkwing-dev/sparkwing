//go:build unix

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestPipelineDescribeRefusesUnusableSourceInsteadOfCachedMetadata(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "stale"}[cached], func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			projectAt(t, "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n")
			dir, err := findSparkwingDir()
			if err != nil {
				t.Fatal(err)
			}
			if cached {
				if err := storeDescribe(dir, "unrelated-source", nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "Deploy", Args: []nodeproto.ArgumentDescription{}}}}); err != nil {
					t.Fatal(err)
				}
			}
			var resultErr error
			out := captureStdout(t, func() {
				resultErr = runPipelineDescribe([]string{"--pipeline=deploy-prod", "-o=json"})
			})
			if resultErr == nil || strings.TrimSpace(out) != "" {
				t.Fatalf("unusable source returned a successful schema: output=%s error=%v", out, resultErr)
			}
		})
	}
}

func TestPipelineDescribeUsesLiveEntrypointSchemaProperty(t *testing.T) {
	dir, registered := aliasProject(t)
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	property := func(value string, secretsOnly bool) bool {
		fresh := registered
		fresh.Extra = true
		fresh.Args = append([]nodeproto.ArgumentDescription{}, registered.Args...)
		fresh.Args[2].Name = "password-" + hex.EncodeToString([]byte(value))
		fresh.Secrets = []nodeproto.SecretDeclaration{{Name: "TOKEN_" + hex.EncodeToString([]byte(value))}}
		raw, err := json.Marshal([]nodeproto.PipelineDescription{
			{Name: "Deploy", Args: []nodeproto.ArgumentDescription{}}, fresh,
		})
		if err != nil {
			t.Fatal(err)
		}
		protocolFixture(t, fixture, counter, string(raw))
		args := []string{"--pipeline=deploy-prod", "-o=json"}
		if secretsOnly {
			args = append(args, "--secrets")
		}
		calls := 0
		var resultErr error
		out := captureStdout(t, func() {
			resultErr = runPipelineDescribeCommand(args, func(name string, inspect func(context.Context, pipelineentry.Command, string) error) error {
				calls++
				if name != "deploy-prod" {
					return errors.New("lost the invoked pipeline")
				}
				return inspect(t.Context(), pipelineentry.Binary(fixture), filepath.Dir(dir))
			})
		})
		var got Pipeline
		if resultErr != nil || calls != 1 || json.Unmarshal([]byte(out), &got) != nil || !reflect.DeepEqual(got.Secrets, fresh.Secrets) {
			return false
		}
		if secretsOnly {
			var selection struct {
				Pipeline string `json:"pipeline"`
			}
			return json.Unmarshal([]byte(out), &selection) == nil && selection.Pipeline == "deploy-prod"
		}
		var description nodeproto.PipelineDescription
		return json.Unmarshal([]byte(out), &description) == nil && description.Extra == fresh.Extra && got.Name == "deploy-prod" && got.Entrypoint == "Deploy" && reflect.DeepEqual(got.Args, fresh.Args) && reflect.DeepEqual(got.Risks, fresh.Risks)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	if n := starts(t, counter); n != 64 {
		t.Fatalf("SDK descriptions=%d, want one for every invocation", n)
	}
}

func TestPipelineDescribePropagatesAcquisitionAndProtocolFailures(t *testing.T) {
	for _, mode := range []string{"acquire", "protocol", "registration", "binding"} {
		t.Run(mode, func(t *testing.T) {
			dir, _ := aliasProject(t)
			fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
			body := `[{"name":"deploy-prod","args":[]}]`
			switch mode {
			case "protocol":
				body = `null`
			case "registration":
				body = `[{"name":"Deploy","args":[]}]`
			case "binding":
				writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    unknown: true\n")
			}
			protocolFixture(t, fixture, counter, body)
			failure := errors.New("source acquisition refused")
			for _, secretsOnly := range []bool{false, true} {
				args := []string{"--pipeline=deploy-prod", "-o=json"}
				if secretsOnly {
					args = append(args, "--secrets")
				}
				var resultErr error
				out := captureStdout(t, func() {
					resultErr = runPipelineDescribeCommand(args, func(_ string, inspect func(context.Context, pipelineentry.Command, string) error) error {
						if mode == "acquire" {
							return failure
						}
						return inspect(t.Context(), pipelineentry.Binary(fixture), filepath.Dir(dir))
					})
				})
				if resultErr == nil || out != "" || mode == "acquire" && !errors.Is(resultErr, failure) {
					t.Fatalf("secrets=%t output=%s error=%v", secretsOnly, out, resultErr)
				}
			}
		})
	}
}

func TestTriggerInspectionRetainsLiveDescribeArgumentsAndClassifications(t *testing.T) {
	input, deps, entry := triggerInspectionFixture(t)
	selection, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	entry.Directory = filepath.Join(input.checkout, ".sparkwing")
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	protocolFixture(t, fixture, counter, `[{"name":"deploy-prod","extra":true,"args":[{"name":"region","type":"string","required":true},{"name":"image","type":"string","required":true,"job_id":"rollout"},{"name":"password","type":"string","required":false,"secret":true}],"risks":["prod"]}]`)
	deps.exec = func(ctx context.Context, dir string, _ []string, program string, args ...string) ([]byte, error) {
		if program != entry.Engine.Executable || dir != entry.Directory {
			t.Fatal("inspection replaced the acquired engine or checkout")
		}
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil || inv.Command.Path != "sparkwing pipeline describe" {
			t.Fatalf("engine invocation=%v error=%v", args, err)
		}
		var commandErr error
		raw := captureStdout(t, func() {
			commandErr = runPipelineDescribeCommand(args[2:], func(_ string, inspect func(context.Context, pipelineentry.Command, string) error) error {
				return inspect(ctx, pipelineentry.Binary(fixture), filepath.Dir(dir))
			})
		})
		return []byte(raw), commandErr
	}
	described, err := inspectTriggerSource(t.Context(), selection, input.environment, deps)
	if err != nil {
		t.Fatal(err)
	}
	requestInput := triggerRequestInput{source: selection, described: described, settings: settings.Inputs{Context: settings.Context{NumCPU: 4}}}
	if len(described) != 1 || len(described[0].Args) != 3 || !described[0].Args[0].Required || !described[0].Args[1].Required || described[0].Args[1].JobID != "rollout" {
		t.Fatalf("live pipeline/job argument metadata disappeared: %+v", described)
	}
	requestInput.args = []string{"--region=west", "--password=private-value", "--image=release", "--extra=value"}
	request, err := prepareTriggerRequest(requestInput)
	if err != nil || request.Request.Pipeline != "deploy-prod" || request.Request.Source.Entrypoint != "Deploy" || request.Request.RequestedArgs["extra"] != "value" || !reflect.DeepEqual(request.Request.SecretArgs.Value, []string{"password"}) {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	if len(described) != 1 || !reflect.DeepEqual(described[0].Risks, []string{"prod"}) || starts(t, counter) != 1 {
		t.Fatalf("SDK description=%+v starts=%d", described, starts(t, counter))
	}
}
