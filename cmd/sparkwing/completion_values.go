package main

func runInternalCompleteTargets(_ []string) error {
	return nil
}

func runInternalCompleteRunners(_ []string) error {
	return nil
}

func runInternalCompleteProfilesForPipeline(_ []string) error {
	return runInternalCompleteProfiles(nil)
}
