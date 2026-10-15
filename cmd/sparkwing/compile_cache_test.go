package main

import (
	"regexp"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestPipelineCacheKey_HyphenFormat(t *testing.T) {
	dir := scaffoldPipelineRepo(t)
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{8}$`).MatchString(key) {
		t.Fatalf("invalid pipeline cache key: %q", key)
	}
}
