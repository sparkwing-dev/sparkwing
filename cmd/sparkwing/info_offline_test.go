package main

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

type infoTestTransport func(*http.Request) (*http.Response, error)

func (f infoTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInfoDoesNotFetchReleaseTips(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	originalVersion, originalTransport := Version, http.DefaultTransport
	t.Cleanup(func() { Version = originalVersion; http.DefaultTransport = originalTransport })
	Version = "v1.2.3"
	var calls atomic.Int32
	http.DefaultTransport = infoTestTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("offline fixture") })
	gatherInfo()
	if calls.Load() != 0 {
		t.Fatalf("local info attempted %d HTTP requests", calls.Load())
	}
}
