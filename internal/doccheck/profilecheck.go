package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"gopkg.in/yaml.v3"
)

// topLevelProfilesRE matches a ```yaml block that is a standalone
// profiles.yaml document -- a top-level `profiles:` key. Blocks that
// also carry `pipelines:` are project sparkwing.yaml documents and are
// validated by checkYAMLConfigs (whose projectconfig.Config embeds the
// same profile types under KnownFields), so this check skips them.
var topLevelProfilesRE = regexp.MustCompile(`(?m)^profiles:\s*$`)

// checkProfileConfigs decodes every standalone profiles.yaml ```yaml
// block with the strict field rules profile.Load's types impose. A doc
// that shows a key the loader doesn't model -- a top-level `default:`, a
// `detect:` block, a flat `controller: <url>` scalar, a `gitcache:` /
// `log_store:` profile key -- fails here instead of silently doing
// nothing (or hard-erroring) in a reader's profiles.yaml.
//
// Migration and proposal docs are version-pinned historical artifacts
// and may describe older or hypothetical shapes; they are excluded, the
// same as the frozen-count check.
func checkProfileConfigs(contentDir string) bool {
	blocks, err := extract(contentDir, "yaml")
	if err != nil {
		fmt.Println("profiles-config: extract error:", err)
		return false
	}

	var checked, failed int
	var failures []string
	for _, b := range blocks {
		if b.skip != "" || !topLevelProfilesRE.MatchString(b.body) {
			continue
		}
		if topLevelPipelinesRE.MatchString(b.body) {
			continue
		}
		if strings.Contains(b.file, "/migrations/") || strings.Contains(b.file, "/proposals/") {
			continue
		}
		checked++
		var cfg profile.Config
		dec := yaml.NewDecoder(strings.NewReader(b.body))
		dec.KnownFields(true)
		if perr := dec.Decode(&cfg); perr != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s:%d\n%s", b.file, b.line, indent(perr.Error())))
		}
	}

	fmt.Printf("doccheck/profiles-config: %d profiles.yaml block(s) -- %d valid, %d INVALID\n",
		checked, checked-failed, failed)
	if failed > 0 {
		fmt.Printf("\n%d profiles.yaml example(s) the loader's types reject (key the loader ignores or a type mismatch):\n\n", failed)
		for _, f := range failures {
			fmt.Println(f)
		}
		return false
	}
	fmt.Println("\nALL profiles.yaml DOC EXAMPLES PARSE")
	return true
}
