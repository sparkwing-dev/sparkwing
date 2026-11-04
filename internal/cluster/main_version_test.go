package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRunnerErrorOutputFingerprintsCredentials(t *testing.T) {
	for _, prefix := range capability.CredentialPrefixes() {
		credential := prefix + strings.Repeat("x", 43)
		var output bytes.Buffer
		writeRunnerError(&output, "warm", fmt.Errorf("invalid response: %s", credential))
		if strings.Contains(output.String(), credential) || !strings.Contains(output.String(), "warm: invalid response: [credential:") {
			t.Errorf("unsafe runner error: %q", output.String())
		}
	}
}

func TestRunnerVersionReportsStampedOfflineIdentity(t *testing.T) {
	var output bytes.Buffer
	if err := runRunnerVersion(&output, []string{"-o", "json", "--offline"}, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("version wrote %d lines, want one JSON record: %q", len(lines), output.String())
	}
	var identity buildinfo.Identity
	if err := json.Unmarshal([]byte(lines[0]), &identity); err != nil {
		t.Fatalf("decode identity: %v (%q)", err, output.String())
	}
	if identity.Binary != "sparkwing-runner" || identity.Version != "v9.9.9" || identity.GOOS != runtime.GOOS || identity.GOARCH != runtime.GOARCH {
		t.Fatalf("stamped runner identity: %+v", identity)
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
	if err := runRunnerVersionIO(&stdout, &diagnostics, []string{"--help", "-o", "plain"}, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if diagnostics.Len() != 0 || bytes.Contains(stdout.Bytes(), []byte(`"binary"`)) {
		t.Fatalf("version help wrote diagnostics %q or identity output %q", diagnostics.String(), stdout.String())
	}
	for _, want := range []string{
		"usage: sparkwing-runner version",
		"--offline",
		"--output",
		"--help-all",
	} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Errorf("version help %q does not contain %q", stdout.String(), want)
		}
	}

	stdout.Reset()
	if err := runRunnerVersionIO(&stdout, &diagnostics, []string{"--help", "-o", "json"}, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	var help struct{ Kind, Text string }
	if err := json.Unmarshal(stdout.Bytes(), &help); err != nil || help.Kind != "help" || !strings.Contains(help.Text, "usage: sparkwing-runner version") {
		t.Fatalf("JSON help = %q (%v), want one help record", stdout.String(), err)
	}
	if err := runRunnerVersionIO(&bytes.Buffer{}, &bytes.Buffer{}, []string{"--help", "-o", "bogus"}, "v1.2.3"); err == nil {
		t.Fatal("help accepted an invalid output mode")
	}

	var topLevel bytes.Buffer
	usage(&topLevel)
	for _, want := range []string{
		"start|warm|run-node|launch|version",
		"run-node - execute a bootstrap-authorized Cloud unit",
		"version  - print this executable's offline build identity",
	} {
		if !bytes.Contains(topLevel.Bytes(), []byte(want)) {
			t.Errorf("top-level usage %q does not contain %q", topLevel.String(), want)
		}
	}
}
