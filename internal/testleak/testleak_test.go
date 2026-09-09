package testleak

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// safety: without this the leaking helper would run in the parent suite and
// fail the package it is meant to prove.
const leakProbeEnv = "SPARKWING_TESTLEAK_PROBE"

func TestMain(m *testing.M) {
	Main(m)
}

// TestHelperLeaksAGoroutine is the payload the child processes run.
func TestHelperLeaksAGoroutine(t *testing.T) {
	if os.Getenv(leakProbeEnv) == "" {
		t.Skip("runs only in the child processes this package spawns")
	}
	blocked := make(chan struct{})
	go func() { <-blocked }()
}

func TestLeakCheckSkipsAHelperReExecOfTheTestBinary(t *testing.T) {
	output, err := runLeakProbe(t, os.Environ())
	if err != nil {
		t.Fatalf("a helper process the tests re-exec failed on its own leak check, which "+
			"the parent can only report as an unexplained helper crash: %v\n%s", err, output)
	}
}

func TestLeakCheckStillFailsASuiteThatLeaks(t *testing.T) {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, hostEnv+"=") {
			env = append(env, entry)
		}
	}

	output, err := runLeakProbe(t, env)
	if err == nil {
		t.Fatalf("a suite that leaked a goroutine passed, so the guard disabled the "+
			"check everywhere rather than in helper processes:\n%s", output)
	}
	if !strings.Contains(output, "found unexpected goroutines") {
		t.Fatalf("the leaking suite failed for some reason other than the leak:\n%s", output)
	}
}

func TestHostMarkerRecognizesOnlyTheBinaryThatSetIt(t *testing.T) {
	t.Setenv(hostEnv, os.Args[0])
	if !markHost() {
		t.Fatal("the test binary did not recognize its own marker, so every helper " +
			"process would run the leak check on itself")
	}

	t.Setenv(hostEnv, os.Args[0]+"-some-other-binary")
	if markHost() {
		t.Fatal("another binary's marker was taken for this one, so a suite nested " +
			"inside a test would silently skip its leak check")
	}
}

func runLeakProbe(t *testing.T, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLeaksAGoroutine$", "-test.v")
	cmd.Env = append(env, leakProbeEnv+"=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
