package launcher

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobScratchIsStickyBeforeRunnerStarts(t *testing.T) {
	l, _, boundary, control := fixture()
	resources, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	scratch, bin := t.TempDir(), t.TempDir()
	if err := os.Chmod(scratch, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"chmod":   "#!/bin/sh\n[ \"$1\" = 1777 ] && [ \"$2\" = /tmp ] || exit 1\nexec /bin/chmod 1777 \"$SCRATCH\"\n",
		JobBinary: "#!/bin/sh\n[ -k \"$SCRATCH\" ] || { echo '/tmp lacks sticky bit'; exit 1; }\nprintf '%s\\n' \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	container := resources.Job.Spec.Template.Spec.Containers[0]
	argv := append([]string{"-c", `exec "$@"`, "job-test"}, container.Command...)
	argv = append(argv, container.Args...)
	cmd := exec.CommandContext(t.Context(), "/bin/sh", argv...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SCRATCH="+scratch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("job startup: %v: %s", err, output)
	}
	mode, err := os.Stat(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o777 || mode.Mode()&os.ModeSticky == 0 {
		t.Fatalf("scratch mode = %s, want 1777", mode.Mode())
	}
	if !strings.HasPrefix(string(output), "run-node\n--controller\nhttps://controller.example\n") || !strings.HasSuffix(string(output), "--job\njob\n") {
		t.Fatalf("runner arguments changed: %s", output)
	}
}

func TestJobPodRetriesDNSAfterOneSecond(t *testing.T) {
	l, _, boundary, control := fixture()
	resources, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	spec := resources.Job.Spec.Template.Spec
	if spec.DNSPolicy != "" || spec.DNSConfig == nil || len(spec.DNSConfig.Nameservers) != 0 || len(spec.DNSConfig.Searches) != 0 {
		t.Fatalf("DNS policy %q, config %+v; want cluster DNS with options only", spec.DNSPolicy, spec.DNSConfig)
	}
	got := map[string]string{}
	for _, o := range spec.DNSConfig.Options {
		got[o.Name] = *o.Value
	}
	if want := map[string]string{"timeout": "1", "attempts": "5"}; !maps.Equal(got, want) {
		t.Fatalf("DNS options = %v, want %v", got, want)
	}
}
