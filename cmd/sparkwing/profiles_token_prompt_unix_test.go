//go:build !windows

package main

import (
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestRestoreEchoOnSignalRestoresTheTerminalBeforeExiting(t *testing.T) {
	restored := make(chan struct{})
	exited := make(chan int, 1)
	original := exitOnSignal
	exitOnSignal = func(code int) {
		exited <- code
		runtime.Goexit()
	}
	t.Cleanup(func() { exitOnSignal = original })

	stop := restoreEchoOnSignal(func() { close(restored) })
	t.Cleanup(stop)

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("raise SIGINT: %v", err)
	}
	select {
	case <-restored:
	case <-time.After(5 * time.Second):
		t.Fatal("an interrupt during the password prompt did not restore terminal echo")
	}
	select {
	case code := <-exited:
		if want := 128 + int(syscall.SIGINT); code != want {
			t.Errorf("exit code = %d, want %d", code, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt handler did not end the process")
	}
}

func TestRestoreEchoOnSignalStopsListeningAfterTheRead(t *testing.T) {
	restored := make(chan struct{}, 1)
	original := exitOnSignal
	exitOnSignal = func(int) { runtime.Goexit() }
	t.Cleanup(func() { exitOnSignal = original })

	stop := restoreEchoOnSignal(func() { restored <- struct{}{} })
	stop()

	observed := make(chan os.Signal, 1)
	signal.Notify(observed, os.Interrupt)
	defer signal.Stop(observed)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("raise SIGINT: %v", err)
	}
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("the test never observed the interrupt")
	}
	select {
	case <-restored:
		t.Error("the prompt handler restored the terminal after the read finished")
	default:
	}
}
