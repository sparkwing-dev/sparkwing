//go:build !linux

package chaos

func homeResidents(string) ([]int, error) { return nil, errResidentsUnsupported }
