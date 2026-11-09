package procenv

import (
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// safety: the system cmd.exe, never COMSPEC or a PATH match, parses the escaped line.
func setCommandLine(cmd *exec.Cmd, line string) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		cmd.Err = err
		return
	}
	cmd.Path = filepath.Join(system, "cmd.exe")
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
}
