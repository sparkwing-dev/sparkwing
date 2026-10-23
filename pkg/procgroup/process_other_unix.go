//go:build !windows && !darwin && !linux

package procgroup

func nativeProcessTable(bool) ([]Info, bool) { return nil, false }
