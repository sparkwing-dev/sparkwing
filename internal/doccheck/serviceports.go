package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type service struct {
	dnsLabel string
	mainFile string
}

var services = []service{
	{"sparkwing-controller", filepath.Join("cmd", "sparkwing-controller", "main.go")},
	{"sparkwing-web", filepath.Join("cmd", "sparkwing-web", "main.go")},
	{"sparkwing-logs", filepath.Join("cmd", "sparkwing-logs", "main.go")},
}

var addrDefaultRE = regexp.MustCompile(`"addr",\s*"[^"]*:(\d+)"`)

var targetPortRE = regexp.MustCompile(`->\s*(\d+)`)

func checkServicePorts(contentDir, repoRoot string) bool {
	canonical := map[string]string{}
	for _, s := range services {
		// #nosec G703 -- a build-time tool reading paths the operator names
		data, err := os.ReadFile(filepath.Join(repoRoot, s.mainFile))
		if err != nil {
			fmt.Printf("service-ports: read %s: %v\n", s.mainFile, err)
			return false
		}
		m := addrDefaultRE.FindStringSubmatch(string(data))
		if m == nil {
			fmt.Printf("service-ports: no default --addr port in %s\n", s.mainFile)
			return false
		}
		canonical[s.dnsLabel] = m[1]
	}

	var mismatches []string
	var checked int
	// #nosec G703 -- a build-time tool reading paths the operator names
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return werr
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time check already trusts
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(contentDir, path)
		for ln, line := range strings.Split(string(data), "\n") {
			for _, s := range services {
				if !strings.Contains(line, s.dnsLabel) {
					continue
				}
				tm := targetPortRE.FindStringSubmatch(line)
				if tm == nil {
					continue
				}
				checked++
				if tm[1] != canonical[s.dnsLabel] {
					mismatches = append(mismatches, fmt.Sprintf(
						"%s:%d: %s targets port %s but its --addr default is %s",
						rel, ln+1, s.dnsLabel, tm[1], canonical[s.dnsLabel]))
				}
			}
		}
		return nil
	})

	fmt.Printf("doccheck/service-ports: %d documented service address(es) -- %d mismatched\n", checked, len(mismatches))
	if len(mismatches) > 0 {
		fmt.Printf("\n%d service port(s) in docs disagreeing with the binary's --addr default:\n", len(mismatches))
		for _, m := range mismatches {
			fmt.Println("  " + m)
		}
		return false
	}
	fmt.Println("\nALL DOCUMENTED SERVICE PORTS MATCH CODE")
	return true
}
