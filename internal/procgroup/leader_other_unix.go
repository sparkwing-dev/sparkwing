//go:build !windows && !linux && !darwin

package procgroup

import "time"

func waitLeaderExit(pid int) error {
	for {
		processes, err := processTable(false)
		if err != nil {
			return err
		}
		for _, process := range processes {
			if process.PID == pid {
				if len(process.State) > 0 && process.State[0] == 'Z' {
					return nil
				}
				goto sleep
			}
		}
		return nil
	sleep:
		time.Sleep(20 * time.Millisecond)
	}
}
