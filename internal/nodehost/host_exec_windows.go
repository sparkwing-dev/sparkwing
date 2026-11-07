package nodehost

import (
	"log/slog"
	"os/exec"
)

// safety: Windows hosts do not offer host exec, so these only serve a misbehaving SDK.
type commandSession struct{ cmd *exec.Cmd }

func configureHostCommand(*exec.Cmd) {}

func trackHostCommand(cmd *exec.Cmd, _ []string) *commandSession { return &commandSession{cmd: cmd} }

func (c *commandSession) kill() {
	if err := c.cmd.Process.Kill(); err != nil {
		slog.Default().Debug("kill SDK command", "err", err)
	}
}

func endCommandSessions([]*commandSession) error { return nil }
