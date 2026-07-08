package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"gopkg.in/yaml.v3"
)

// topLevelPipelinesRE matches a ```yaml block that is a sparkwing.yaml
// config document -- i.e. one with a top-level `pipelines:` key. Other
// yaml blocks in the docs (profiles.yaml, GitHub Actions, Helm values,
// backend specs) don't have this key and are left alone.
var topLevelPipelinesRE = regexp.MustCompile(`(?m)^pipelines:\s*$`)

// checkYAMLConfigs decodes every sparkwing.yaml-shaped ```yaml block
// under contentDir with the same strict field rules the CLI's
// projectconfig.Load uses (KnownFields on the full project Config + the
// per-pipeline UnmarshalYAML unknown-field rejection). A doc that shows
// an unknown/renamed config key or a removed trigger fails here instead
// of hard-erroring in a reader's repo.
//
// Unlike projectconfig.Load it deliberately skips normalize/validate:
// doc examples legitimately abbreviate (a triggers-only fragment with no
// entrypoint), and the value here is catching key drift, not enforcing
// completeness. Returns false on any decode failure.
func checkYAMLConfigs(contentDir string) bool {
	blocks, err := extract(contentDir, "yaml")
	if err != nil {
		fmt.Println("yaml-config: extract error:", err)
		return false
	}

	var configs, failed int
	var failures []string
	for _, b := range blocks {
		if b.skip != "" || !topLevelPipelinesRE.MatchString(b.body) {
			continue
		}
		configs++
		var cfg projectconfig.Config
		dec := yaml.NewDecoder(strings.NewReader(b.body))
		dec.KnownFields(true)
		if perr := dec.Decode(&cfg); perr != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s:%d\n%s", b.file, b.line, indent(perr.Error())))
		}
	}

	fmt.Printf("doccheck/yaml-config: %d sparkwing.yaml block(s) -- %d valid, %d INVALID\n",
		configs, configs-failed, failed)
	if failed > 0 {
		fmt.Printf("\n%d sparkwing.yaml example(s) the strict parser rejects (would hard-error on load):\n\n", failed)
		for _, f := range failures {
			fmt.Println(f)
		}
		return false
	}
	fmt.Println("\nALL sparkwing.yaml DOC EXAMPLES PARSE")
	return true
}
