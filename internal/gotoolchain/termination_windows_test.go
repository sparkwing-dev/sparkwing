//go:build windows

package gotoolchain

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRunWindowsAttributesTermination(t *testing.T) {
	for _, mode := range []string{"owner", "external", "exit23"} {
		t.Run(mode, func(t *testing.T) {
			for trial := 0; trial < 8; trial++ {
				base, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				ctx, stop := context.WithCancel(base)
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				input, send, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				command := exec.Command(os.Args[0], "-test.run=^TestRunWindowsChild$", "--", "child")
				command.Stdout, command.Stdin = writer, input
				inherited := &windowsTerminationContext{Context: ctx, command: command, ready: bufio.NewReader(reader), send: send, mode: mode, cancel: stop}
				err = Run(inherited, command)
				stop()
				cancel()
				_ = reader.Close()
				_ = writer.Close()
				_ = input.Close()
				_ = send.Close()
				var exit *exec.ExitError
				if mode == "owner" {
					if !errors.Is(err, context.Canceled) || errors.As(err, &exit) {
						t.Fatalf("owner termination: %v", err)
					}
				} else {
					want := 1
					if mode == "exit23" {
						want = 23
					}
					if !errors.As(err, &exit) || exit.ExitCode() != want {
						t.Fatalf("%s status lost: %v", mode, err)
					}
				}
			}
		})
	}
}

func TestRunWindowsChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "child" {
		return
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	var input [1]byte
	if _, err := os.Stdin.Read(input[:]); err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}

type windowsTerminationContext struct {
	context.Context
	command *exec.Cmd
	ready   *bufio.Reader
	send    *os.File
	mode    string
	cancel  context.CancelFunc
	once    sync.Once
}

func (c *windowsTerminationContext) Done() <-chan struct{} {
	c.once.Do(func() {
		line, err := c.ready.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "ready" {
			panic(fmt.Sprintf("child readiness: %q %v", line, err))
		}
		if c.mode != "owner" {
			process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(c.command.Process.Pid))
			if err != nil {
				panic(err)
			}
			defer windows.CloseHandle(process)
			if c.mode == "exit23" {
				_, err = c.send.Write([]byte{'x'})
			} else {
				err = c.command.Process.Kill()
			}
			if err != nil {
				panic(err)
			}
			if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err != nil {
				panic(err)
			}
		}
		c.cancel()
	})
	return c.Context.Done()
}
