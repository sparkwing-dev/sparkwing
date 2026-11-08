package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type DesktopUpdate struct{ sparkwing.Base }

func (DesktopUpdate) ShortHelp() string {
	return "Update the desktop from green main after runs and land units finish"
}

func (DesktopUpdate) Help() string {
	return "Start the sparkwing-desktop-update user unit. It builds green origin/main in a private staging directory, waits up to 30 minutes for the default and delivery controllers and active land units to become idle, then replaces ~/.local/bin/sparkwing and sparkwing-runner and restarts the running controllers. Inspection failures and drain timeouts leave installed binaries unchanged. Read activation results with journalctl --user -u sparkwing-desktop-update."
}

func (*DesktopUpdate) Plan(_ context.Context, plan *sparkwing.Plan, _ struct{}, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "handoff", &desktopUpdateJob{RepoDir: sparkwing.WorkDir()}).
		Inline().Cores(lightJobCores).Timeout(time.Minute)
	return nil
}

type desktopUpdateJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *desktopUpdateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "start-updater", func(ctx context.Context) error { return j.run(ctx, false) }).
		DryRun(func(ctx context.Context) error { return j.run(ctx, true) })
	return nil, nil
}

func (j *desktopUpdateJob) run(ctx context.Context, dry bool) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("desktop update requires Linux or WSL with a systemd user manager")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	state := filepath.Join(home, ".sparkwing")
	homes := []string{state}
	if delivery := os.Getenv("SPARKWING_HOME"); delivery != "" && delivery != state {
		if !filepath.IsAbs(delivery) {
			return fmt.Errorf("desktop update requires an absolute SPARKWING_HOME")
		}
		homes = append(homes, delivery)
	}
	// safety: the updater must outlive this run before it can restart the controller that owns the run.
	args := []string{
		"--user", "--collect", "--unit=sparkwing-desktop-update", "--property=Type=exec",
		"--setenv=HOME=" + home, "--setenv=PATH=" + os.Getenv("PATH"),
		"--", "bash", filepath.Join(j.RepoDir, "bin", "desktop-update.sh"),
		j.RepoDir, filepath.Join(home, ".local", "bin"), "1800",
	}
	args = append(args, homes...)
	if dry {
		sparkwing.Info(ctx, "would start systemd-run %q", args)
		return nil
	}
	_, err = sparkwing.Exec(ctx, "systemd-run", args...).Run()
	if err != nil {
		return fmt.Errorf("start desktop updater: %w", err)
	}
	sparkwing.Info(ctx, "activation handed to sparkwing-desktop-update.service; inspect journalctl --user -u sparkwing-desktop-update")
	return nil
}

func init() {
	sparkwing.Register[struct{}]("DesktopUpdate", func() sparkwing.Pipeline[struct{}] { return &DesktopUpdate{} })
}
