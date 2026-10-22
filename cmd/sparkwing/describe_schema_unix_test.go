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

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
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
				if err := storeDescribe(dir, "unrelated-source", presentationFixture(nodeproto.PipelineDescription{Name: "Deploy", Args: []nodeproto.ArgumentDescription{}})); err != nil {
					t.Fatal(err)
				}
			}
			var resultErr error
			out := captureStdout(t, func() {
				resultErr = runPipelineDescribeArgs([]string{"--pipeline=deploy-prod", "-o=json"})
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
			{Name: "deploy-prod", Args: []nodeproto.ArgumentDescription{}}, fresh,
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
			resultErr = runPipelineDescribeCommandArgs(args, func(name string, inspect func(context.Context, pipelineentry.Command, string) error) error {
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
			body := `[{"name":"Deploy","args":[]}]`
			switch mode {
			case "protocol":
				body = `null`
			case "registration":
				body = `[{"name":"deploy-prod","args":[]}]`
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
					resultErr = runPipelineDescribeCommandArgs(args, func(_ string, inspect func(context.Context, pipelineentry.Command, string) error) error {
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
