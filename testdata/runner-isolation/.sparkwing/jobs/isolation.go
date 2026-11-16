// Package jobs holds the pipeline the runner isolation proof submits to a
// dedicated-user runner. Its one job reports what the claimed job could see,
// as its output, and never the contents of anything it read.
package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Probe is the job's output. Read maps each target path to "allowed" or the
// error that refused it.
type Probe struct {
	UID         int               `json:"uid"`
	EUID        int               `json:"euid"`
	GID         int               `json:"gid"`
	User        string            `json:"user"`
	Home        string            `json:"home"`
	NoNewPrivs  string            `json:"no_new_privs"`
	Read        map[string]string `json:"read"`
	HomeEntries int               `json:"home_entries"`
	HomeListErr string            `json:"home_list_err"`
	TmpMarker   string            `json:"tmp_marker"`
	SudoExit    int               `json:"sudo_exit"`
}

// The proof workflow plants these before it submits the run.
var targets = []string{
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
		p := Probe{UID: os.Getuid(), EUID: os.Geteuid(), GID: os.Getgid(), Home: os.Getenv("HOME"), Read: map[string]string{}, SudoExit: -1}
		if u, err := user.Current(); err == nil {
			p.User = u.Username
		}
		p.NoNewPrivs = statusField("NoNewPrivs")
		for _, target := range targets {
			p.Read[target] = readable(target)
		}
		entries, err := os.ReadDir("/home")
		p.HomeEntries = len(entries)
		if err != nil {
			p.HomeListErr = err.Error()
		}
		if err := os.WriteFile(TmpMarkerPath, []byte(strconv.Itoa(p.UID)+"\n"), 0o600); err != nil {
			p.TmpMarker = err.Error()
		} else {
			p.TmpMarker = "written"
		}
		// safety: -n never prompts; with NoNewPrivileges the setuid bit cannot raise the job.
		err = exec.CommandContext(ctx, "sudo", "-n", "true").Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			p.SudoExit = 0
		case errors.As(err, &exit):
			p.SudoExit = exit.ExitCode()
		}
		sparkwing.Info(ctx, "runner-isolation-probe uid=%d user=%s", p.UID, p.User)
		return p, nil
	}), nil
}

func readable(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	if info.IsDir() {
		if _, err := os.ReadDir(path); err != nil {
			return err.Error()
		}
		return "allowed"
	}
	f, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	_ = f.Close()
	return "allowed"
}

func statusField(name string) string {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err.Error()
	}
	for _, line := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return "absent"
}

type isolation struct{ sparkwing.Base }

func (isolation) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "probe-isolation", &isolationProbe{})
	return nil
}

func init() {
	sparkwing.Register("runner-isolation", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &isolation{} })
}
