package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// service pairs a cluster DNS label with the binary whose default
// `--addr` flag is the single source of truth for the port that label's
// Service targets. The docs' internal-address table cites these; when a
// service's bind port changes in the binary, the table rots silently.
type service struct {
	dnsLabel string
	mainFile string
}

var services = []service{
	{"sparkwing-controller", filepath.Join("cmd", "sparkwing-controller", "main.go")},
	{"sparkwing-web", filepath.Join("cmd", "sparkwing-web", "main.go")},
	{"sparkwing-logs", filepath.Join("cmd", "sparkwing-logs", "main.go")},
}

// addrDefaultRE captures the port from a service binary's default
// `--addr` value, e.g. `fs.String("addr", "127.0.0.1:4344", ...)`.
var addrDefaultRE = regexp.MustCompile(`"addr",\s*"[^"]*:(\d+)"`)

// targetPortRE captures the port a doc line says a Service maps to: the
// arrow form `80 -> 4344` used in the internal-address table.
var targetPortRE = regexp.MustCompile(`->\s*(\d+)`)

// checkServicePorts verifies that wherever a doc line names a cluster
// service by its DNS label and states the port that Service targets
// (`... -> <port>`), the port matches the service binary's default
// `--addr` bind port. This anchors the internal-address documentation to
// a single source of truth in code and catches the class of drift where
// a service's port changed but the address table still shows the old
// number. Returns false on any mismatch.
func checkServicePorts(contentDir, repoRoot string) bool {
	canonical := map[string]string{}
	for _, s := range services {
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
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return werr
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
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
