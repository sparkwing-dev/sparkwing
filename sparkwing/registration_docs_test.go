package sparkwing

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const registrationExampleMarker = "<!-- registration-example -->"

var (
	docRegisterRE = regexp.MustCompile(`(?:sparkwing|sw)\.Register(?:Entrypoint)?(?:\[[^\]]*\])?\("([^"]+)"`)
	docFenceRE    = regexp.MustCompile("(?s)```(go|yaml)\n(.*?)```")
)

type registrationExamplePipeline struct{ invoked *string }

func (p registrationExamplePipeline) Plan(_ context.Context, plan *Plan, _ NoInputs, rc RunContext) error {
	*p.invoked = rc.Pipeline
	Job(plan, "deploy", func(context.Context) error { return nil })
	return nil
}

// TestDocumentedRegistrationExamplesPlan plans each marked Go and YAML pair the
// way the host does: the CLI resolves the YAML entry, and the SDK looks up its
// entrypoint among the keys the Go block registered.
func TestDocumentedRegistrationExamplesPlan(t *testing.T) {
	for _, path := range []string{"../docs/authoring-pipelines.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sections := strings.Split(string(data), registrationExampleMarker)
		if len(sections) < 2 {
			t.Fatalf("%s has no %s", path, registrationExampleMarker)
		}
		for _, section := range sections[1:] {
			blocks := map[string]string{}
			for _, m := range docFenceRE.FindAllStringSubmatch(section, -1) {
				if _, seen := blocks[m[1]]; !seen {
					blocks[m[1]] = m[2]
				}
			}
			checkRegistrationExample(t, path, blocks["go"], blocks["yaml"])
		}
	}
}

func checkRegistrationExample(t *testing.T, path, goBlock, yamlBlock string) {
	t.Helper()
	keys := docRegisterRE.FindAllStringSubmatch(goBlock, -1)
	if len(keys) == 0 || yamlBlock == "" {
		t.Fatalf("%s: registration example needs a Go Register call and a YAML block", path)
	}
	cfg, err := projectconfig.Parse([]byte(yamlBlock))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	isolateProtocolDiscoveryRegistry(t)
	var invoked string
	for _, key := range keys {
		Register(key[1], func() Pipeline[NoInputs] { return registrationExamplePipeline{invoked: &invoked} })
	}
	for _, pipeline := range cfg.Pipelines {
		identity, err := clisurface.ResolvePipeline(cfg, pipeline.Name)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, _, err := (sdkOperations{}).Plan(context.Background(), identity.Entrypoint, map[string]string{}, nodeproto.RunContext{RunID: "run-doc", Pipeline: identity.Name}); err != nil {
			t.Fatalf("%s: pipeline %q (entrypoint %q) does not plan against the documented registration: %v", path, identity.Name, identity.Entrypoint, err)
		}
		if invoked != identity.Name {
			t.Fatalf("%s: RunContext.Pipeline = %q, want invoked name %q", path, invoked, identity.Name)
		}
	}
}
