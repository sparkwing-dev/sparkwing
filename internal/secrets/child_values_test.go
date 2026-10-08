package secrets_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const (
	maskFixtureEnv    = "SPARKWING_TEST_MASK_FIXTURE"
	maskFixtureSecret = "fixture-secret-7c41e9"
)

// safety: this plays a pipeline process that prints a registered secret
// around the SDK, the way author code can.
func runMaskFixture(mode string) {
	secrets.ShareRegisteredFromEnv()
	m := secrets.NewMasker()
	switch mode {
	case "direct":
		m.Register(maskFixtureSecret)
		fmt.Println("stdout " + maskFixtureSecret)
		fmt.Fprintln(os.Stderr, "stderr "+maskFixtureSecret)
		child := exec.Command("sh", "-c", `echo "child $FIXTURE_VALUE"`)
		child.Env = append(os.Environ(), "FIXTURE_VALUE="+maskFixtureSecret)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture child:", err)
			os.Exit(3)
		}
		go func() { panic("boom " + maskFixtureSecret) }()
		select {}
	case "late":
		for i := range 500 {
			fmt.Printf("noise %d\n", i)
		}
		m.Register(maskFixtureSecret)
		fmt.Fprintln(os.Stderr, "late "+maskFixtureSecret)
		fmt.Print("tail " + maskFixtureSecret)
	}
	os.Exit(0)
}

func runMaskedFixture(t *testing.T, mode string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows passes no extra descriptor to a child")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), maskFixtureEnv+"="+mode)
	values, err := secrets.ShareWithChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = values.Writer(&stdout)
	cmd.Stderr = values.Writer(&stderr)
	_ = cmd.Run()
	values.Close()
	return stdout.String(), stderr.String()
}

func TestShareWithChildMasksOutputAroundTheSDK(t *testing.T) {
	stdout, stderr := runMaskedFixture(t, "direct")
	all := stdout + stderr
	if strings.Contains(all, maskFixtureSecret) {
		t.Fatalf("secret reached the launcher's output:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{"stdout ***", "child ***"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{"stderr ***", "panic: boom ***"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestShareWithChildMasksValuesRegisteredAfterOutputStarted(t *testing.T) {
	stdout, stderr := runMaskedFixture(t, "late")
	if strings.Contains(stdout+stderr, maskFixtureSecret) {
		t.Fatalf("secret reached the launcher's output:\nstderr:\n%s\nstdout tail:\n%s", stderr, stdout[max(0, len(stdout)-200):])
	}
	if !strings.Contains(stdout, "noise 499\n") || !strings.HasSuffix(stdout, "tail ***") {
		t.Errorf("stdout lost lines or the unterminated tail:\n%s", stdout[max(0, len(stdout)-200):])
	}
	if !strings.Contains(stderr, "late ***") {
		t.Errorf("stderr lacks the masked late line:\n%s", stderr)
	}
}
