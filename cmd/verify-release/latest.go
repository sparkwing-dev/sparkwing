package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/mod/semver"
)

func publishedReleasePages() ([]byte, error) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is required to list published releases")
	}
	// #nosec G702 -- gh receives one repos/-prefixed endpoint argument without a shell or option expansion.
	body, err := exec.Command("gh", "api", "--paginate", "--slurp", "repos/"+repo+"/releases?per_page=100").Output()
	if err != nil {
		return nil, fmt.Errorf("list published releases: %w", err)
	}
	return body, nil
}

func latestPublishedRelease(tag string, body []byte) (bool, error) {
	if !semver.IsValid(tag) {
		return false, fmt.Errorf("invalid release version %q", tag)
	}
	var pages [][]struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &pages); err != nil {
		return false, fmt.Errorf("read published releases: %w", err)
	}
	if len(pages) == 0 {
		return false, fmt.Errorf("published releases response contains no pages")
	}
	eligible := semver.Prerelease(tag) == ""
	for _, page := range pages {
		if page == nil {
			return false, fmt.Errorf("published releases response contains a null page")
		}
		for _, release := range page {
			if release.Draft || release.Prerelease || !semver.IsValid(release.Tag) || semver.Prerelease(release.Tag) != "" {
				continue
			}
			if semver.Compare(tag, release.Tag) <= 0 {
				eligible = false
			}
		}
	}
	return eligible, nil
}
