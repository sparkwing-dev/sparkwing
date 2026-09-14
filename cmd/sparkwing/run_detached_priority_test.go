package main

import (
	"strings"
	"testing"
)

func TestRunDetached_RefusesAnUnusablePriority(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	args := append(e.detachArgs("fixture", "--sw-output", "json"), "--sw-priority", "sideways")
	_, errOut, err := e.runStdout(args...)
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(errOut, "--sw-priority") {
		t.Fatalf("the refusal does not name the flag: %s", errOut)
	}
}

const priorityFixtureSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"fixture\"}]")
		return
	}
	fmt.Println("observed-priority=" + os.Getenv("SPARKWING_PRIORITY"))
}
`

func TestDispatchRun_RefusesAnUnusablePriorityBeforeAnythingElse(t *testing.T) {
	err := dispatchRun([]string{"no-such-pipeline", "--sw-priority", "sideways"})
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(err.Error(), "--sw-priority") {
		t.Fatalf("dispatchRun = %v, want the flag named", err)
	}
}
