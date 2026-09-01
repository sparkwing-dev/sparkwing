package main

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

var topLevelProfilesRE = regexp.MustCompile(`(?m)^profiles:\s*$`)

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
