//go:build !darwin && !linux

package wingd

func backgroundProcess(int) error { return nil }
