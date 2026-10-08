//go:build !windows

package bincache

import "context"

func legacyRetirementBusy(context.Context, string, error) (bool, error) { return false, nil }
