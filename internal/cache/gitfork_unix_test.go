//go:build !windows

package cache

import "testing"

func installNativeGitForkCounter(*testing.T, string, string, string) bool { return false }
