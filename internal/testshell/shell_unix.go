//go:build !windows

package testshell

import "testing"

func nativeInstall(*testing.T, string, string) (string, bool) { return "", false }
