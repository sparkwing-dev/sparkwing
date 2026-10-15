package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

var topLevelProfilesRE = regexp.MustCompile(`(?m)^profiles:\s*`)

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
		if strings.Contains(b.file, "/migrations/") {
			continue
		}
		checked++
		if _, perr := userconfig.Decode(b.file, []byte(b.body)); perr != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s:%d\n%s", b.file, b.line, indent(perr.Error())))
		}
	}

	fmt.Printf("doccheck/profiles-config: %d profiles block(s) -- %d valid, %d INVALID\n",
		checked, checked-failed, failed)
	if failed > 0 {
		fmt.Printf("\n%d profiles example(s) the machine decoder rejects:\n\n", failed)
		for _, f := range failures {
			fmt.Println(f)
		}
		return false
	}
	fmt.Printf("\nALL profiles DOC EXAMPLES PARSE\n")
	return true
}
