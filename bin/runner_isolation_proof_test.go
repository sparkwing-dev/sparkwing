package main

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The proof creates OS users and system services, so it must refuse every
// host that is not a discarded GitHub-hosted VM before it writes anything.
// Full runs are exercised only where an Actions variable is missing, since
// this suite itself runs on hosted VMs; the cases with every variable set use
// --preflight, which checks the same facts and never writes.
func TestRunnerIsolationProofRefusesOutsideDisposableRunner(t *testing.T) {
	testProofRefusesOutsideDisposableRunner(t, "runner-isolation-proof.sh")
}

func testProofRefusesOutsideDisposableRunner(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	hosted := []string{"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted"}
	ownedRecord := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ownedRecord, "imagegeneration"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownedRecord, "imagegeneration", "imagedata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	type refusal struct {
		name    string
		env     []string
		args    []string
		refuses []string
	}
	cases := []refusal{
		{name: "run without Actions", args: []string{"EVIDENCE"}, refuses: []string{"GITHUB_ACTIONS"}},
		{name: "run on a self-hosted runner", env: []string{"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=self-hosted"}, args: []string{"EVIDENCE"}, refuses: []string{"RUNNER_ENVIRONMENT"}},
		{name: "every variable set, no host facts", env: hosted, args: []string{"--preflight", t.TempDir()}, refuses: []string{"imagedata.json", "Runner.Worker"}},
		{name: "every variable set, image record not root-owned", env: hosted, args: []string{"--preflight", ownedRecord}, refuses: []string{"imagedata.json", "Runner.Worker"}},
	}
	if _, err := os.Stat("/imagegeneration/imagedata.json"); errors.Is(err, os.ErrNotExist) {
		cases = append(cases, refusal{name: "every variable set on this host", env: hosted, args: []string{"--preflight"}, refuses: []string{"imagedata.json", "Runner.Worker"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if os.Getuid() == 0 && tc.name == "every variable set, image record not root-owned" {
				t.Skip("as root the planted record is root-owned")
			}
			dir := t.TempDir()
			args := []string{script}
			for _, arg := range tc.args {
				if arg == "EVIDENCE" {
					arg = filepath.Join(dir, "evidence")
				}
				args = append(args, arg)
			}
			cmd := exec.Command("bash", args...)
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "RUNNER_TEMP=" + dir}, tc.env...)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("exit = %v, want 2\n%s", err, output)
			}
			if !strings.Contains(string(output), "RESULT: REFUSED") {
				t.Fatalf("refusal not reported:\n%s", output)
			}
			for _, reason := range tc.refuses {
				if !strings.Contains(string(output), "refused: ") || !strings.Contains(string(output), reason) {
					t.Fatalf("refusal does not name %s:\n%s", reason, output)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("refusal wrote %d entries under RUNNER_TEMP", len(entries))
			}
		})
	}
}

func TestRunnerIsolationProofWorkflowIsManualReadOnlyAndSecretFree(t *testing.T) {
	testProofWorkflow(t, "runner-isolation-proof")
}

func testProofWorkflow(t *testing.T, proof string) {
	t.Helper()
	body, err := os.ReadFile("../.github/workflows/" + proof + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secrets.") {
		t.Fatal("the proof workflow must not read secrets")
	}
	var doc struct {
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			RunsOn      string            `yaml:"runs-on"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	triggers := make([]string, 0, len(doc.On))
	for trigger := range doc.On {
		triggers = append(triggers, trigger)
	}
	if !slices.Equal(triggers, []string{"workflow_dispatch"}) {
		t.Fatalf("triggers = %v, want workflow_dispatch only", triggers)
	}
	readOnly := map[string]string{"contents": "read"}
	if len(doc.Permissions) != 1 || doc.Permissions["contents"] != "read" {
		t.Fatalf("workflow permissions = %v, want %v", doc.Permissions, readOnly)
	}
	if len(doc.Jobs) != 1 {
		t.Fatalf("jobs = %d, want one", len(doc.Jobs))
	}
	for name, job := range doc.Jobs {
		if len(job.Permissions) != 1 || job.Permissions["contents"] != "read" {
			t.Fatalf("%s permissions = %v, want %v", name, job.Permissions, readOnly)
		}
		if job.RunsOn != "ubuntu-24.04" {
			t.Fatalf("%s runs on %q; the proof needs a disposable GitHub-hosted Ubuntu VM", name, job.RunsOn)
		}
		runs := false
		uploads := false
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
				t.Fatalf("%s checkout leaves the job token on disk", name)
			}
			runs = runs || strings.Contains(step.Run, "bin/"+proof+".sh")
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				uploads = step.If == "${{ always() }}" && step.With["if-no-files-found"] == "error" && step.With["path"] != ""
			}
		}
		if !runs {
			t.Fatalf("%s never runs bin/%s.sh", name, proof)
		}
		if !uploads {
			t.Fatalf("%s must upload evidence even when the proof fails", name)
		}
	}
}

const fakeControllerName = "sparkwing-controller-proof"

func runFakeController(args []string) int {
	addr := ""
	for i, arg := range args {
		if arg == "--addr" && i+1 < len(args) {
			addr = args[i+1]
		}
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return 1
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		_ = listener.Close()
	}()
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	return 0
}

// safety: evaluates only the script's deadline helper and controller functions, never its guard or privileged body.
const lifecycleDriver = `set -uo pipefail
SCRIPT=$1 BIN=$2 PORT=$3 CTL=$4
EVIDENCE=$CTL CONTROLLER="http://127.0.0.1:$PORT" CONTROLLER_PID=""
eval "$(sed -n '/^deadline()/,/^}/p' "$SCRIPT")"
eval "$(sed -n '/^ctl_env()/,/^# Team mode refuses/p' "$SCRIPT")"
declare -F deadline start_controller stop_controller >/dev/null || { echo "the script defines no controller lifecycle"; exit 3; }
step() {
  local name=$1
  shift
  if "$@"; then echo "$name=ok"; else echo "$name=fail"; fi
}
step start1 start_controller local
echo "pid1=$CONTROLLER_PID"
echo "exe1=$(readlink "/proc/$CONTROLLER_PID/exe")"
step stop1 stop_controller
echo "health_after_stop=$(curl --silent --output /dev/null --max-time 2 --write-out '%{http_code}' "$CONTROLLER/api/v1/health")"
echo "listeners_after_stop=$(ss -ltnH "( sport = :$PORT )" | wc -l | tr -d ' ')"
step start2 start_controller team
echo "pid2=$CONTROLLER_PID"
echo "exe2=$(readlink "/proc/$CONTROLLER_PID/exe")"
step stop2 stop_controller
`

// The hosted proof once backgrounded its controller through a shell function,
// so $! named a subshell: stopping it left the controller serving the port and
// the next start refused. This drives the script's real start and stop against
// a fake controller and checks the recorded PID, the released port and the
// restart.
func TestRunnerIsolationProofControllerLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the controller lifecycle reads /proc and ss")
	}
	for _, tool := range []string{"bash", "curl", "ss", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable: %v", tool, err)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, fakeControllerName)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, self, fake)
	t.Cleanup(func() { killProcessesOf(t, fake) })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	script, err := filepath.Abs("runner-isolation-proof.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", lifecycleDriver, "driver", script, bin, strconv.Itoa(port), dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("driver: %v\n%s", err, output)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && !strings.Contains(key, " ") {
			got[key] = value
		}
	}
	for _, step := range []string{"start1", "stop1", "start2", "stop2"} {
		if got[step] != "ok" {
			t.Errorf("%s = %q, want ok\n%s", step, got[step], output)
		}
	}
	for _, n := range []string{"1", "2"} {
		if got["exe"+n] != fake {
			t.Errorf("start %s recorded pid %q running %q, want the controller %s", n, got["pid"+n], got["exe"+n], fake)
		}
	}
	if got["health_after_stop"] != "000" {
		t.Errorf("health after stop = %q, want no answer (000)", got["health_after_stop"])
	}
	if got["listeners_after_stop"] != "0" {
		t.Errorf("listeners after stop = %q, want 0", got["listeners_after_stop"])
	}
	if left := processesOf(fake); len(left) != 0 {
		t.Errorf("controllers still running after the final stop: %v", left)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, body, 0o755); err != nil {
		t.Fatal(err)
	}
}

func processesOf(path string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil && exe == path {
			pids = append(pids, pid)
		}
	}
	return pids
}

func killProcessesOf(t *testing.T, path string) {
	t.Helper()
	for _, pid := range processesOf(path) {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
			_, _ = process.Wait()
		}
	}
}
