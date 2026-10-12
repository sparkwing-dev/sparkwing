package main

import "runtime"

func nativeSocketTestParent() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return "/tmp"
}
