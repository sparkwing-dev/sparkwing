package cluster

import (
	"bytes"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
)

func TestRunnerVersionReportsStampedOfflineIdentity(t *testing.T) {
	var output bytes.Buffer
	if err := runRunnerVersion(&output, []string{"-o", "json", "--offline"}, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	var identity buildinfo.Identity
	if err := json.Unmarshal(output.Bytes(), &identity); err != nil {
		t.Fatalf("decode identity: %v (%q)", err, output.String())
	}
	expected := buildinfo.Expectation{
		Binary: "sparkwing-runner", Version: "v9.9.9",
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	if err := buildinfo.Verify(identity, expected); err != nil {
		t.Fatal(err)
	}
	expected.Version = "v9.9.8"
	if err := buildinfo.Verify(identity, expected); err == nil {
		t.Fatal("stamped runner matched a different required release")
	}
}

func TestRunnerVersionOutputModes(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"-o", "plain", "--offline"}, "v1.2.3\n"},
		{[]string{"-o", "pretty"}, "sparkwing-runner v1.2.3 ("},
	} {
		var output bytes.Buffer
		if err := runRunnerVersion(&output, test.args, "v1.2.3"); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(output.Bytes(), []byte(test.want)) {
			t.Errorf("output %q does not contain %q", output.String(), test.want)
		}
	}
	if err := runRunnerVersion(&bytes.Buffer{}, []string{"-o", "yaml"}, "v1.2.3"); err == nil {
		t.Error("runner version accepted an unknown output format")
	}
}

func TestRunnerVersionHelpAndTopLevelUsage(t *testing.T) {
	var stdout, diagnostics bytes.Buffer
	if err := runRunnerVersionIO(&stdout, &diagnostics, []string{"--help"}, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("version help wrote identity output: %q", stdout.String())
	}
	for _, want := range []string{
		"usage: sparkwing-runner version",
		"--offline",
		"--output",
		"pretty | json | plain",
	} {
		if !bytes.Contains(diagnostics.Bytes(), []byte(want)) {
			t.Errorf("version help %q does not contain %q", diagnostics.String(), want)
		}
	}

	var topLevel bytes.Buffer
	usage(&topLevel)
	for _, want := range []string{"runner|worker|agent|version", "version - print this executable's offline build identity"} {
		if !bytes.Contains(topLevel.Bytes(), []byte(want)) {
			t.Errorf("top-level usage %q does not contain %q", topLevel.String(), want)
		}
	}
}
