package main

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

var topLevelProfilesRE = regexp.MustCompile(`(?m)^profiles:\s*$`)

func checkProfileConfigs(documentationDirectory string) bool {
	blocks, err := extract(documentationDirectory, "yaml")
	if err != nil {
		fmt.Println("profiles-config: extract error:", err)
		return false
	}

	var checked, failed int
	var failures []string
	for _, example := range blocks {
		if example.skip != "" || !topLevelProfilesRE.MatchString(example.body) {
			continue
		}
		if topLevelPipelinesRE.MatchString(example.body) {
			continue
		}
		if strings.Contains(example.file, "/migrations/") || strings.Contains(example.file, "/proposals/") {
			continue
		}
		checked++
		var config profile.Config
		decoder := yaml.NewDecoder(strings.NewReader(example.body))
		decoder.KnownFields(true)
		if decodeError := decoder.Decode(&config); decodeError != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s:%d\n%s", example.file, example.line, indent(decodeError.Error())))
		}
	}

	fmt.Printf("doccheck/profiles-config: %d profiles.yaml block(s) -- %d valid, %d INVALID\n",
		checked, checked-failed, failed)
	if failed > 0 {
		fmt.Printf("\n%d profiles.yaml example(s) failed strict YAML decoding:\n\n", failed)
		for _, failure := range failures {
			fmt.Printf("%s\n", failure)
		}
		return false
	}
	fmt.Println("\nALL profiles.yaml DOC EXAMPLES PARSE")
	return true
}
