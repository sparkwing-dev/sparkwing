//go:build windows

package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestKubernetesE2EWindowsProcessHelper(t *testing.T) {
	if os.Getenv("K8S_TEST_PROCESS_HELPER") != "1" {
		return
	}
	response, err := http.Get(os.Getenv("K8S_TEST_READY_URL") + "?pid=" + strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil {
		t.Fatal(err)
	}
}

func TestKubernetesE2EWindowsCancellationEndsProcessTree(t *testing.T) {
	bound, stop := context.WithTimeout(t.Context(), 20*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(bound)
	defer cancel()
	ready := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		pid, err := strconv.Atoi(request.URL.Query().Get("pid"))
		if err != nil {
			http.Error(w, "invalid pid", http.StatusBadRequest)
			return
		}
		ready <- pid
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8S_TEST_PROCESS_HELPER", "1")
	t.Setenv("K8S_TEST_HELPER", filepath.ToSlash(executable))
	t.Setenv("K8S_TEST_READY_URL", server.URL)
	root := t.TempDir()
	script := filepath.Join(root, "kubernetes owned descendant.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n\"$K8S_TEST_HELPER\" -test.run=^TestKubernetesE2EWindowsProcessHelper$ &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runKubernetesE2EScript(ctx, root, script) }()
	var pid int
	select {
	case pid = <-ready:
	case err := <-done:
		t.Fatalf("Kubernetes process exited before readiness: %v", err)
	case <-bound.Done():
		t.Fatal("Kubernetes descendant never became ready")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Kubernetes command succeeded")
		}
	case <-bound.Done():
		t.Fatal("Kubernetes command did not honor cancellation")
	}
	if state, err := windows.WaitForSingleObject(process, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("Kubernetes descendant remains: state=%d error=%v", state, err)
	}
}
