package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunReadersDistinguishMissingRunFromEmptyRun(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", filepath.Join(t.TempDir(), "profiles.yaml"))
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(context.Background(), store.Run{ID: "present", Pipeline: "fixture", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	readers := map[string]func(string) error{
		"logs": func(id string) error {
			return orchestrator.JobLogs(context.Background(), paths, id, orchestrator.LogsOpts{}, &bytes.Buffer{})
		},
		"errors": func(id string) error {
			return orchestrator.JobErrors(context.Background(), paths, id, false, &bytes.Buffer{})
		},
		"annotations": func(id string) error {
			_, err := listLocalAnnotations(context.Background(), paths, id, "", "", false)
			return err
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			if err := read("present"); err != nil {
				t.Fatalf("empty existing run: %v", err)
			}
			if err := read("missing"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("missing run error=%v", err)
			}
		})
	}
}

func TestRemoteAnnotationsChecksRunBeforeChildren(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/nodes") {
			w.Write([]byte(`{"nodes":[]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	writeProfilesFixture(t, "profiles:\n  remote:\n    controller: {url: "+srv.URL+"}\n")
	_, err := listRemoteAnnotations(context.Background(), "remote", "missing", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing remote run error=%v", err)
	}
}
