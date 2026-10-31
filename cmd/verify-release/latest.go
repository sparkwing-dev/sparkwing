package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func publishedReleasePages() ([]byte, error) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is required to list published releases")
	}
	// #nosec G702 -- gh receives one repos/-prefixed endpoint argument without a shell or option expansion.
	body, err := procenv.HostCommand("gh", "api", "--paginate", "--slurp", "repos/"+repo+"/releases?per_page=100").Output()
	if err != nil {
		return nil, fmt.Errorf("list published releases: %w", err)
	}
	return body, nil
}

func latestPublishedRelease(tag string, body []byte) (bool, error) {
	if !semver.IsValid(tag) {
		return false, fmt.Errorf("invalid release version %q", tag)
	}
	pages, err := decodeReleasePages(body)
	if err != nil {
		return false, err
	}
	eligible := semver.Prerelease(tag) == ""
	for _, page := range pages {
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

type publishedRelease struct {
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func decodeReleasePages(body []byte) ([][]publishedRelease, error) {
	var pages [][]publishedRelease
	if err := json.Unmarshal(body, &pages); err != nil {
		return nil, fmt.Errorf("read published releases: %w", err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("published releases response contains no pages")
	}
	for _, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("published releases response contains a null page")
		}
	}
	return pages, nil
}
