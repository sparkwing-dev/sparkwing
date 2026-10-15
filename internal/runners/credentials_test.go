package runners

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestFileBearerRequiresANonemptyCredential(t *testing.T) {
	for _, machine := range []bool{true, false} {
		if _, err := FileBearer("", machine); err == nil {
			t.Fatal("missing credentials directory was accepted")
		}
		directory := t.TempDir()
		if _, err := FileBearer(directory, machine); err == nil {
			t.Fatal("missing credential file was accepted")
		}
		if err := fssecure.WriteFile(filepath.Join(directory, "agent-token"), []byte(" \n\t")); err != nil {
			t.Fatal(err)
		}
		if _, err := FileBearer(directory, machine); err == nil {
			t.Fatal("empty credential file was accepted")
		}
	}
}

func TestFileBearerRetainsTheStartupCredential(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent-token")
	const raw = "private-startup-credential"
	if err := fssecure.WriteFile(path, []byte(raw+"\n")); err != nil {
		t.Fatal(err)
	}
	for _, machine := range []bool{true, false} {
		bearer, err := FileBearer(directory, machine)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := bearer(t.Context()); got != raw || err != nil {
			t.Fatalf("credential was not retained: %v", err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		cause := errors.New("transport stopped")
		cancel(cause)
		if got, err := bearer(ctx); got != "" || !errors.Is(err, cause) || strings.Contains(err.Error(), raw) {
			t.Fatalf("cancelled transport exposed authority: %v", err)
		}
	}
}
