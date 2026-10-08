//go:build !windows

package sparks

import "testing"

func nativeSparksGo(*testing.T, string, string) (string, bool) { return "", false }
