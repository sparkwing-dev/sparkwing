package nodehost

import (
	"log/slog"
	"os"
	"os/exec"
)

// safety: Windows hosts do not offer host exec, so these only serve a misbehaving SDK.
type commandSession struct{ cmd *exec.Cmd }

func configureHostCommand(*exec.Cmd) {}

func execFormatError(error) bool { return false }

func bufferedBytes(*os.File) int { return 0 }

func trackHostCommand(cmd *exec.Cmd, _ []string) *commandSession { return &commandSession{cmd: cmd} }

func (c *commandSession) leader() (int, string, bool) { return 0, "", false }

func (c *commandSession) kill() {
	if err := c.cmd.Process.Kill(); err != nil {
		slog.Default().Debug("kill SDK command", "err", err)
	}
}

func endCommandSessions([]*commandSession) error { return nil }
