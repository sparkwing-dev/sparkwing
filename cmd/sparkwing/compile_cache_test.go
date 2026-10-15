package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

var validCacheHash = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{8}){0,3}$`)

func TestPipelineCacheKey_HyphenFormat(t *testing.T) {
	dir := scaffoldPipelineRepo(t)
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatalf("pipelineCacheKey: %v", err)
	}
	if !validCacheHash.MatchString(key) {
		t.Errorf("key %q does not match cache regex", key)
	}
	if !strings.Contains(key, "-") {
		t.Errorf("key %q missing hyphen separator", key)
	}
}
