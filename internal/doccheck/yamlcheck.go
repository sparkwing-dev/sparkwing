package main

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

var topLevelPipelinesRE = regexp.MustCompile(`(?m)^pipelines:\s*$`)

func checkYAMLConfigs(documentationDirectory string) bool {
	blocks, err := extract(documentationDirectory, "yaml")
	if err != nil {
		fmt.Println("yaml-config: extract error:", err)
		return false
	}

	var configs, failed int
	var failures []string
	for _, example := range blocks {
		if example.skip != "" || !topLevelPipelinesRE.MatchString(example.body) {
			continue
		}
		configs++
		var config projectconfig.Config
		decoder := yaml.NewDecoder(strings.NewReader(example.body))
		decoder.KnownFields(true)
		if decodeError := decoder.Decode(&config); decodeError != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s:%d\n%s", example.file, example.line, indent(decodeError.Error())))
		}
	}

	fmt.Printf("doccheck/yaml-config: %d sparkwing.yaml block(s) -- %d valid, %d INVALID\n",
		configs, configs-failed, failed)
	if failed > 0 {
		fmt.Printf("\n%d sparkwing.yaml example(s) failed strict YAML decoding:\n\n", failed)
		for _, failure := range failures {
			fmt.Printf("%s\n", failure)
		}
		return false
	}
	return true
}
