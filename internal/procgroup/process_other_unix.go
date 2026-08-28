//go:build !windows && !darwin

package procgroup

func nativeProcessTable(bool) ([]Info, bool) { return nil, false }
