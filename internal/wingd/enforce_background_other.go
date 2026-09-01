//go:build !darwin

package wingd

func backgroundProcess(int) error { return nil }
