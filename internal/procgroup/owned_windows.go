//go:build windows

package procgroup

func killOwnedGroup(int) {}

func forwardTerminationToOwned() {}
