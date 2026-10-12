//go:build windows

package supervise

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const supervisorConsoleProbeEnv = "SPARKWING_TEST_SUPERVISOR_CONSOLE_PROBE"

func TestSupervisorNativeConsoleHelper(t *testing.T) {
	endpoint := os.Getenv(supervisorConsoleProbeEnv)
	if endpoint == "" {
		return
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("invalid console probe endpoint")
	}
	conn, err := net.DialTimeout("tcp", endpoint, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	if _, err := fmt.Fprintf(conn, "%d\n", console); err != nil {
		t.Fatal(err)
	}
	var release [1]byte
	if _, err := io.ReadFull(conn, release[:]); err != nil || release[0] != 'q' {
		t.Fatalf("console probe release: %v", err)
	}
	fmt.Fprintln(os.Stdout, "supervisor stdout retained")
	fmt.Fprintln(os.Stderr, "supervisor stderr retained")
}

func TestSupervisorNativeChildHasNoConsoleAndRetainsLogs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorConsoleProbeEnv, listener.Addr().String())
	readLog, writeLog, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readLog.Close()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = writeLog, writeLog
	child, startErr := startExecChild(os.Args[0], []string{"-test.run=^TestSupervisorNativeConsoleHelper$"})
	os.Stdout, os.Stderr = oldStdout, oldStderr
	_ = writeLog.Close()
	if startErr != nil {
		t.Fatal(startErr)
	}
	defer func() {
		if !child.(*execChild).reaped.Load() {
			cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCleanup()
			_ = child.Kill()
			select {
			case <-child.Wait():
			case <-cleanupCtx.Done():
				t.Error("console probe child survived retained-handle cleanup")
			}
		}
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var console uint64
	if _, err := fmt.Fscanln(conn, &console); err != nil {
		t.Fatal(err)
	}
	if console != 0 {
		t.Fatalf("supervised child allocated console HWND %d", console)
	}
	if _, err := conn.Write([]byte{'q'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-child.Wait():
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	logs, err := io.ReadAll(readLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"supervisor stdout retained", "supervisor stderr retained"} {
		if !strings.Contains(string(logs), want) {
			t.Fatalf("native child logs omitted %q: %s", want, logs)
		}
	}
}

func TestConfigureSupervisorChildPreservesProcessFlags(t *testing.T) {
	cmd := exec.Command("unused.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	attrs := cmd.SysProcAttr
	configureSupervisorChild(cmd)
	if cmd.SysProcAttr != attrs || !attrs.HideWindow || attrs.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW {
		t.Fatalf("existing process attributes changed: %+v", cmd.SysProcAttr)
	}
}
