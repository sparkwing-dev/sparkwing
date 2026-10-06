//go:build !windows

package bincache

import "testing"

func installNativeFakeGo(*testing.T, string, string, bool, string, string) bool { return false }

func installNativeSSHShim(*testing.T, string, string) bool { return false }

func testNativeActiveLegacyWriter(*testing.T) bool { return false }
