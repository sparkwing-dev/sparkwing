// Package jobs holds the pipeline the runner isolation proof submits to a
// dedicated-user runner. Its one job reports what the claimed job could see,
// as its output, and never the contents of anything it read.
package jobs

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Outcomes a probe reports. Anything else is "error: <text>", a failed
// measurement rather than evidence of isolation.
const (
	Allowed = "allowed"
	Denied  = "denied"
	Absent  = "absent"
)

// Probe is the job's output. Read maps each target path to an outcome.
// HomeList is "empty", "entries:N", Denied, Absent or an error. Sudo fields
// separate a sudo that never launched (SudoLaunchErr) from one that ran and
// exited SudoExit.
type Probe struct {
	UID           int               `json:"uid"`
	EUID          int               `json:"euid"`
	GID           int               `json:"gid"`
	User          string            `json:"user"`
	Home          string            `json:"home"`
	NoNewPrivs    string            `json:"no_new_privs"`
	Read          map[string]string `json:"read"`
	HomeList      string            `json:"home_list"`
	TmpMarker     string            `json:"tmp_marker"`
	SudoRan       bool              `json:"sudo_ran"`
	SudoExit      int               `json:"sudo_exit"`
	SudoLaunchErr string            `json:"sudo_launch_err"`
	SudoStderr    string            `json:"sudo_stderr"`
}

// Targets the proof workflow plants or that exist on every hosted VM.
var Targets = []string{
	"/home/alice/.ssh/id_ed25519",
	"/home/alice/.aws/credentials",
	"/home/alice/notes.txt",
	"/home/runner",
	"/root",
	"/tmp/alice-shared-tmp-marker",
}

// TmpMarkerPath is written by the job; outside PrivateTmp it would land in the host's /tmp.
const TmpMarkerPath = "/tmp/sparkwing-isolation-job-marker"

type isolationProbe struct {
	sparkwing.Base
	sparkwing.Produces[Probe]
}

func (j *isolationProbe) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "probe", func(ctx context.Context) (Probe, error) {
		p := Probe{UID: os.Getuid(), EUID: os.Geteuid(), GID: os.Getgid(), Home: os.Getenv("HOME"), Read: map[string]string{}}
		if u, err := user.Current(); err == nil {
			p.User = u.Username
		}
		p.NoNewPrivs = statusField("NoNewPrivs")
		for _, target := range Targets {
			p.Read[target] = readable(target)
		}
		p.HomeList = listing("/home")
		p.TmpMarker = outcome(os.WriteFile(TmpMarkerPath, []byte(strconv.Itoa(p.UID)+"\n"), 0o600))
		p.SudoRan, p.SudoExit, p.SudoLaunchErr, p.SudoStderr = trySudo(ctx)
		sparkwing.Info(ctx, "runner-isolation-probe uid=%d user=%s", p.UID, p.User)
		return p, nil
	}), nil
}

// trySudo runs sudo -n true and reports whether it ran, its exit code, a launch
// error, and the start of its stderr, which names the refusal reason.
func trySudo(ctx context.Context) (ran bool, exit int, launchErr, stderr string) {
	var errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "sudo", "-n", "true")
	cmd.Stderr = &errOut
	err := cmd.Run()
	if len(errOut.Bytes()) > 300 {
		errOut.Truncate(300)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return true, 0, "", errOut.String()
	case errors.As(err, &exitErr):
		return true, exitErr.ExitCode(), "", errOut.String()
	default:
		return false, -1, err.Error(), errOut.String()
	}
}

// Outcome classifies err as Allowed, Denied, Absent or a failed measurement.
func outcome(err error) string {
	switch {
	case err == nil:
		return Allowed
	case errors.Is(err, fs.ErrPermission):
		return Denied
	case errors.Is(err, fs.ErrNotExist):
		return Absent
	default:
		return "error: " + err.Error()
	}
}

func readable(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return outcome(err)
	}
	if info.IsDir() {
		_, err = os.ReadDir(path)
		return outcome(err)
	}
	f, err := os.Open(path)
	if err == nil {
		_ = f.Close()
	}
	return outcome(err)
}

func listing(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return outcome(err)
	}
	if len(entries) == 0 {
		return "empty"
	}
	return "entries:" + strconv.Itoa(len(entries))
}

func statusField(name string) string {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "error: " + err.Error()
	}
	for _, line := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return Absent
}

type isolation struct{ sparkwing.Base }

func (isolation) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "probe-isolation", &isolationProbe{})
	return nil
}

func init() {
	sparkwing.Register("runner-isolation", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &isolation{} })
}
