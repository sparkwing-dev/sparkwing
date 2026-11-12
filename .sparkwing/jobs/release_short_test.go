package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseShortStepUsesSharedPackageTimeout(t *testing.T) {
	for _, failRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("root-fails=%v", failRoot), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if err := os.Mkdir(filepath.Join(root, "sparkwing"), 0o700); err != nil {
				t.Fatal(err)
			}
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			commands := filepath.Join(root, "commands")
			t.Setenv("RELEASE_SHORT_COMMANDS", commands)
			t.Setenv("RELEASE_SHORT_FAIL_ROOT", fmt.Sprint(failRoot))
			writeDeliveryTool(t, filepath.Join(tools, "git"), `#!/bin/sh
case "$*" in
 *ls-files*go.mod*) printf 'go.mod\nsparkwing/go.mod\n' ;;
 *merge-base*) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
 *hash-object*) cat >/dev/null; echo fixture ;;
 *rev-parse*) echo fixture ;;
 *) exit 0 ;;
esac
`)
			writeDeliveryTool(t, filepath.Join(tools, "go"), `#!/bin/sh
case "$1" in
 list)
  case "$PWD" in
   */sparkwing) echo 'example/sdk 1' ;;
   *) printf 'github.com/sparkwing-dev/sparkwing/internal/store 1\nexample/root/other 1\n' ;;
  esac ;;
 build) exit 0 ;;
 test)
  printf '%s|%s|%s\n' "$PWD" "$GOMAXPROCS" "$*" >> "$RELEASE_SHORT_COMMANDS"
  case "$PWD" in
   */sparkwing) exit 0 ;;
   *) [ "$RELEASE_SHORT_FAIL_ROOT" != true ] ;;
  esac ;;
 *) exit 91 ;;
esac
`)
			for _, tool := range []string{"golangci-lint", "pnpm", "npm"} {
				writeDeliveryTool(t, filepath.Join(tools, tool), "#!/bin/sh\nexit 0\n")
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			host, plan, doc := prepareWork(t, &releaseCutChecksJob{})
			step := findStep(doc, "test")
			if step == nil || len(step.Needs) != 1 || step.Needs[0] != "sdk-typescript" {
				t.Fatalf("release test step lost its SDK prerequisite: %+v", step)
			}
			for _, prerequisite := range []string{"build", "lint", "frontend-deps", "sdk-typescript"} {
				if _, err := host.RunStep(t.Context(), plan, "job", prerequisite); err != nil {
					t.Fatalf("release prerequisite %s: %v", prerequisite, err)
				}
			}
			_, runErr := host.RunStep(t.Context(), plan, "job", "test")
			if (runErr != nil) != failRoot {
				t.Fatalf("test result=%v, want root failure %v", runErr, failRoot)
			}
			body, err := os.ReadFile(commands)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(body)), "\n")
			if len(lines) != 2 {
				t.Fatalf("release omitted or repeated a module: %s", body)
			}
			for i, line := range lines {
				t.Logf("release short command: %s", line)
				fields := strings.SplitN(line, "|", 3)
				if len(fields) != 3 || !strings.Contains(fields[2], "-short") || !strings.Contains(fields[2], "-timeout "+productGoTestTimeout) {
					t.Errorf("release bypassed the shared bounded short-test policy: %s", line)
				}
				if len(fields) == 3 && (fields[1] != fmt.Sprint(goStepParallelism(currentHost())) || !strings.Contains(fields[2], "-p "+fields[1])) {
					t.Errorf("release changed its admitted command width: %s", line)
				}
				want := storeTestPackage
				if i == 1 {
					want = "example/sdk"
				}
				if !strings.Contains(line, want) {
					t.Errorf("module coverage changed: %s, missing %s", line, want)
				}
			}
		})
	}
}
