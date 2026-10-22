package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func aliasProject(t *testing.T) (string, nodeproto.PipelineDescription) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	projectAt(t, "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n  - name: deploy-stage\n    entrypoint: Deploy\n")
	dir, err := findSparkwingDir()
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(dir, "go.mod"), "module aliasfixture\n\ngo 1.26\n")
	schema := nodeproto.PipelineDescription{
		Name: "Deploy", Short: "Deploy a service", Help: "Deploys the selected service.",
		Args: []nodeproto.ArgumentDescription{
			{Name: "region", Short: "r", Type: "string", Required: true},
			{Name: "image", Type: "string", Required: true, JobID: "rollout"},
			{Name: "password", Type: "string", Secret: true},
		},
		Secrets: []nodeproto.SecretDeclaration{{Name: "DEPLOY_TOKEN"}},
		Risks:   []string{"prod"},
	}
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	stage := schema
	stage.Name = "Deploy"
	if err := storeDescribe(dir, key, presentationFixture(schema, stage)); err != nil {
		t.Fatal(err)
	}
	return dir, schema
}

func TestPipelineRiskGateUsesRegisteredDeclaration(t *testing.T) {
	_, schema := aliasProject(t)
	bound, err := boundPipelineSchema([]nodeproto.PipelineDescription{schema}, clisurface.PipelineIdentity{Name: "deploy-prod", Entrypoint: "Deploy"})
	if err != nil {
		t.Fatal(err)
	}
	err = enforceRiskGate("deploy-prod", risksIn(bound), runFlags{})
	var blocked *riskBlockedError
	if !errors.As(err, &blocked) || blocked.Pipeline != "deploy-prod" || !reflect.DeepEqual(blocked.MissingLabels, []string{"prod"}) {
		t.Fatalf("pipeline bypassed registered risks: %v", err)
	}
}

func FuzzPipelineAliasMetadataIsolation(f *testing.F) {
	f.Add([]byte("deploy-prod"), []byte("Deploy"), false)
	f.Add([]byte("other"), []byte("release"), true)
	f.Fuzz(func(t *testing.T, aliasBytes, entryBytes []byte, reverse bool) {
		alias, entry := "alias-"+hex.EncodeToString(aliasBytes), "Entry"+hex.EncodeToString(entryBytes)
		cfg := &projectconfig.Config{Pipelines: []projectconfig.Pipeline{{Name: alias, Entrypoint: entry}}}
		identity, err := clisurface.ResolvePipeline(cfg, alias)
		if err != nil {
			t.Fatal(err)
		}
		schemas := []nodeproto.PipelineDescription{
			{Name: entry, Args: []nodeproto.ArgumentDescription{{Name: "password", Type: "string", Secret: true}}, Risks: []string{"prod"}},
			{Name: alias, Args: []nodeproto.ArgumentDescription{{Name: "password", Type: "string"}}},
		}
		if reverse {
			schemas[0], schemas[1] = schemas[1], schemas[0]
		}
		schema, err := boundPipelineSchema(schemas, identity)
		if err != nil || schema.Name != alias || len(schema.Args) != 1 || !schema.Args[0].Secret {
			t.Fatalf("alias changed secret classification: %+v %v", schema, err)
		}
		err = enforceRiskGate(alias, risksIn(schema), runFlags{})
		var blocked *riskBlockedError
		if !errors.As(err, &blocked) || blocked.Pipeline != alias || !reflect.DeepEqual(blocked.MissingLabels, []string{"prod"}) {
			t.Fatalf("pipeline bypassed registered risks: %v", err)
		}
	})
}

func TestPipelineAliasResolvesFlagsAndHelp(t *testing.T) {
	_, registered := aliasProject(t)
	schema, err := boundPipelineSchema([]nodeproto.PipelineDescription{registered}, clisurface.PipelineIdentity{Name: "deploy-prod", Entrypoint: "Deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != "deploy-prod" || !reflect.DeepEqual(schema.Args, registered.Args) {
		t.Fatalf("invoked schema: %+v", schema)
	}
	args, err := parsePipelineArgs(schema, []string{"-r", "us-east", "--image", "release", "--password", "private"})
	if err != nil || !maps.Equal(args, map[string]string{"region": "us-east", "image": "release", "password": "private"}) {
		t.Fatalf("aliased arguments: %v %v", args, err)
	}
	var out bytes.Buffer
	printPipelineHelp(&out, runspec.PipelinePresentation(schema))
	for _, want := range []string{"sparkwing run deploy-prod --region <string> --image <string>", "[from job rollout]", "[secret]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in help: %s", want, &out)
		}
	}
	if _, err := parsePipelineArgs(schema, []string{"--typo", "value"}); err == nil || !strings.Contains(err.Error(), "sparkwing run deploy-prod --help") {
		t.Fatalf("alias diagnostic: %v", err)
	}
	if registered.Name != "Deploy" {
		t.Fatal("binding changed the registered schema")
	}
}

func TestPipelineAliasFlagsUseRegisteredName(t *testing.T) {
	dir, registered := aliasProject(t)
	flags, err := pipelineFlagsFromCache(t.Context(), dir, "deploy-prod")
	if err != nil || !reflect.DeepEqual(flags, registered.Args) {
		t.Fatalf("alias flags: %+v %v", flags, err)
	}
}

func TestPipelineAliasDoesNotFallBackToAnUnrelatedRegistration(t *testing.T) {
	dir, registered := aliasProject(t)
	registered.Name = "deploy-prod"
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeDescribe(dir, key, presentationFixture(registered)); err != nil {
		t.Fatal(err)
	}
	if _, err := boundPipelineSchema([]nodeproto.PipelineDescription{registered}, clisurface.PipelineIdentity{Name: "deploy-prod", Entrypoint: "Deploy"}); err == nil || !strings.Contains(err.Error(), "deploy-prod") {
		t.Fatalf("missing registration used the Go type name: %v", err)
	}
	if _, err := pipelineFlagsFromCache(t.Context(), dir, "deploy-prod"); err == nil {
		t.Fatal("completion used an unrelated registration")
	}
}

func TestPipelineAliasRefusesInvalidBindingConfig(t *testing.T) {
	dir, _ := aliasProject(t)
	writeRepoFile(t, filepath.Join(dir, projectconfig.Filename), "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    typo: private-value\n")
	if _, err := pipelineIdentityAt(dir, "deploy-prod"); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("binding parser: %v", err)
	}
	if _, err := pipelineFlagsFromCache(t.Context(), dir, "deploy-prod"); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("completion binding parser: %v", err)
	}
	if _, err := gatherPipelinesCatalog(true); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("catalog binding parser: %v", err)
	}
}
