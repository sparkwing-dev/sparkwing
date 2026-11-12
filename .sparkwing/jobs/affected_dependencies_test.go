package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAffectedTestsPrepareTypeScriptDependencies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pkg       string
		critical  bool
		installed bool
		docsOnly  bool
	}{
		{name: "acceptance-short", pkg: "tests/acceptance"},
		{name: "nativehost-short", pkg: "sdk/typescript/test/nativehost"},
		{name: "acceptance-critical", pkg: "tests/acceptance", critical: true},
		{name: "nativehost-critical", pkg: "sdk/typescript/test/nativehost", critical: true},
		{name: "acceptance-prepared", pkg: "tests/acceptance", installed: true},
		{name: "nativehost-prepared", pkg: "sdk/typescript/test/nativehost", installed: true},
		{name: "changelog-only", pkg: "tests/acceptance", critical: true, docsOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			compiler := filepath.Join(root, "sdk", "typescript", "node_modules", "typescript", "bin", "tsc")
			marker := ""
			if tc.critical {
				marker = criticalTestMarker + "\n"
			}
			writeGoFile(t, filepath.Join(root, filepath.FromSlash(tc.pkg), "dependency_test.go"), fmt.Sprintf("package fixturetests\nimport (\"os\"; \"testing\")\n%sfunc TestCompiler(t *testing.T) { if _, err := os.Stat(%q); err != nil { t.Fatal(err) } }\n", marker, compiler))
			gitCommitAll(t, root, "dependency fixture")
			writeGoFile(t, filepath.Join(root, "web", "node_modules", ".modules.yaml"), "")
			if tc.installed {
				writeGoFile(t, compiler, "")
			}
			bin := t.TempDir()
			log := filepath.Join(root, "npm-args")
			writeDeliveryTool(t, filepath.Join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(log)+"\n/bin/mkdir -p "+shellQuote(filepath.Dir(compiler))+"\n: > "+shellQuote(compiler)+"\n")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			files := []string{tc.pkg + "/dependency_test.go"}
			if tc.critical {
				files = []string{"internal/sound.go"}
			}
			if tc.docsOnly {
				files = []string{"CHANGELOG.md"}
			}
			ctx := newTestContext(t)
			for range 2 {
				if err := runAffectedTestFiles(ctx, files, "dependency fixture", currentHost(), false); err != nil {
					t.Fatal(err)
				}
			}
			args, err := os.ReadFile(log)
			if tc.installed || tc.docsOnly {
				if !os.IsNotExist(err) {
					t.Fatalf("prepared or unselected dependencies ran npm: %q, %v", args, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.TrimSpace(string(args)), "--prefix sdk/typescript ci --ignore-scripts --no-audit --no-fund"; got != want {
				t.Fatalf("npm calls = %q, want exactly one %q", got, want)
			}
		})
	}
}
