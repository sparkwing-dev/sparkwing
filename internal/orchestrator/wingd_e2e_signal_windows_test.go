//go:build windows

package orchestrator

import "golang.org/x/sys/windows"

func signalSelfInterruptForTest() error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0)
}
