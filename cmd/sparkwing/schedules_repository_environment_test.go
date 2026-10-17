package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScheduleDiscoverySupportsAuthenticatedProxy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor arg do\ncase \"$arg\" in\nremote) echo https://github.com/acme/repo.git; exit 0;;\nsymbolic-ref) echo main; exit 0;;\nesac\ndone\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HTTPS_PROXY", "https://alice:schedule-private-password@proxy.example")
	result, err := readScheduleRepository(t.Context(), dir, true)
	if err != nil || result.Branch != "main" || result.RepoURL != "https://github.com/acme/repo.git" {
		t.Fatalf("discovery: %+v, %v", result, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf '%s' schedule-private-password >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = previous; _ = reader.Close(); _ = writer.Close() }()
	_, err = readScheduleRepository(t.Context(), dir, true)
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil || strings.Contains(err.Error(), "schedule-private-password") || string(output) != "***" {
		t.Fatalf("unmasked discovery sinks: error=%v stderr=%q", err, output)
	}
}
