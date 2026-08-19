// Value-completion compatibility helpers.
//
// Each helper prints one entry per line to stdout and exits zero.
// The shell wrappers in completion.go feed them into _describe.
// Silent failure (empty output, exit 0) is the contract: if the
// underlying file can't be read, the menu falls back to "no values"
// rather than spamming an error during completion.
package main

// runInternalCompleteTargets remains callable by completion scripts
// generated before pipeline targets were removed.
func runInternalCompleteTargets(_ []string) error {
	return nil
}

// runInternalCompleteRunners remains callable by completion scripts
// generated before profile runner selection was removed.
func runInternalCompleteRunners(_ []string) error {
	return nil
}

// runInternalCompleteProfilesForPipeline emits the full profile list.
func runInternalCompleteProfilesForPipeline(_ []string) error {
	return runInternalCompleteProfiles(nil)
}
