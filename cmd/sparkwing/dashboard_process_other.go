//go:build !linux && !darwin

package main

import "errors"

func dashboardBoot() (string, error) {
	return "", errors.New("verified dashboard ownership is unavailable on this platform")
}
func dashboardRunningArtifact() dashboardArtifact { return dashboardArtifact{} }
func stopOwnedDashboard(dashboardRecord) error {
	return errors.New("verified dashboard stop is unavailable on this platform; no PID was signaled")
}
