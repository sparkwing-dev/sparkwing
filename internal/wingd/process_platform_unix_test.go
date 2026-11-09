//go:build !windows

package wingd_test

func processFixtureTempRoot() string { return "/tmp" }

func processFixtureSuffix() string { return "" }
