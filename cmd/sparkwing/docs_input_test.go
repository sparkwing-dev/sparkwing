package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestDocsGuideRejectsIgnoredSourceFlags(t *testing.T) {
	for _, extra := range [][]string{{"--web"}, {"--version", "v0.1.0"}, {"--no-cache"}} {
		var err error
		captureStdout(t, func() { err = runDocsRead(append([]string{"--guide", "authoring"}, extra...)) })
		if err == nil || !strings.Contains(err.Error(), "--guide") {
			t.Fatalf("%v error=%v", extra, err)
		}
	}
}

func TestDocsMigrationsValidatesWebBoundsBeforeFetching(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.Write([]byte("[]")) }))
	defer srv.Close()
	t.Setenv(docs.BaseURLEnvVar, srv.URL)
	for _, args := range [][]string{{"--from", "bogus", "--to", "v1.2.3"}, {"--from", "v1.0.0", "--to", "bogus"}} {
		var err error
		captureStdout(t, func() { err = runDocsMigrationsBetween(append(args, "--web", "--no-cache")) })
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("%v error=%v", args, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("invalid bounds fetched web index %d times", calls.Load())
	}
}
