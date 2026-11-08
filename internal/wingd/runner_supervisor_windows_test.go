//go:build windows

package wingd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const (
	runnerProbeRole    = "SPARKWING_TEST_DETACHED_RUNNER_ROLE"
	runnerProbeAddress = "SPARKWING_TEST_DETACHED_RUNNER_ADDRESS"
)

func init() {
	if os.Getenv(runnerProbeRole) != "runner" {
		return
	}
	connection, err := net.Dial("tcp", os.Getenv(runnerProbeAddress))
	if err != nil {
		panic(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	peer, err := controllerPeerIdentity()
	if err != nil {
		panic(err)
	}
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(connection).Encode(struct {
		PID      int
		Birth    string
		SID      string
		Args     []string
		Captured string
		Home     string
	}{os.Getpid(), birth, peer.SID, os.Args[1:], os.Getenv("CAPTURED"), os.Getenv("SPARKWING_HOME")}); err != nil {
		panic(err)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		panic(err)
	}
	fmt.Print("runner stdout\n")
	fmt.Fprint(os.Stderr, "runner stderr\n")
	if _, err := fmt.Fprint(connection, line); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func TestWindowsRunnerControllerProbe(t *testing.T) {
	if os.Getenv(runnerProbeRole) != "controller" {
		return
	}
	connection, err := net.Dial("tcp", os.Getenv(runnerProbeAddress))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if os.Getenv("SPARKWING_TEST_ALLOW_BREAKAWAY") == "1" {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		t.Fatal(err)
	}
	host := crontimer.Host{GOOS: "windows", Home: os.Getenv("USERPROFILE"), SparkwingHome: os.Getenv("SPARKWING_HOME"), Binary: os.Args[0], PathEnv: os.Getenv("PATH"), Env: make(map[string]string)}
	for _, assignment := range os.Environ() {
		key, value, ok := strings.Cut(assignment, "=")
		if ok && key != "" {
			host.Env[key] = value
		}
	}
	host.Env[runnerProbeRole] = "runner"
	ctx, cancel := context.WithCancel(t.Context())
	err = StartLocalRunnerProcess(ctx, host, "instance")
	cancel()
	result := "started"
	if err != nil {
		result = err.Error()
	}
	if err := json.NewEncoder(connection).Encode(result); err != nil {
		t.Fatal(err)
	}
	var stop [1]byte
	_, _ = connection.Read(stop[:])
	os.Exit(0)
}

func TestWindowsLocalRunnerSurvivesControllerJobAndStartupCancellation(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("breakaway=%t", allowed), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			environment := append(os.Environ(), runnerProbeRole+"=controller", runnerProbeAddress+"="+listener.Addr().String(), "SPARKWING_HOME="+home, "SPARKWING_TEST_ALLOW_BREAKAWAY=0", "CAPTURED=literal %cash% & quotes\" $value")
			if allowed {
				environment = append(environment, "SPARKWING_TEST_ALLOW_BREAKAWAY=1")
			}
			controller := procenv.ControlCommandContext(ctx, environment, os.Args[0], "-test.run=^TestWindowsRunnerControllerProbe$")
			controller.Stdout, controller.Stderr = os.Stdout, os.Stderr
			if err := controller.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = controller.Process.Kill(); _ = controller.Wait() }()
			control, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			_ = control.SetDeadline(time.Now().Add(20 * time.Second))
			var result string
			if err := json.NewDecoder(control).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if !allowed {
				if result == "started" {
					t.Fatal("runner launched inside a job that forbids breakaway")
				}
				return
			}
			if result != "started" {
				t.Fatal(result)
			}
			runner, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			_ = runner.SetDeadline(time.Now().Add(20 * time.Second))
			var report struct {
				PID      int
				Birth    string
				SID      string
				Args     []string
				Captured string
				Home     string
			}
			if err := json.NewDecoder(runner).Decode(&report); err != nil {
				t.Fatal(err)
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(report.PID))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process)
			defer windows.TerminateProcess(process, 1)
			peer, err := controllerPeerIdentity()
			if err != nil {
				t.Fatal(err)
			}
			if report.SID != peer.SID || report.Home != home || report.Captured != "literal %cash% & quotes\" $value" || strings.Join(report.Args, "|") != strings.Join([]string{"wingd", "runner", "--home", home, "--instance", "instance"}, "|") {
				t.Fatalf("runner account/environment/argv changed: %+v", report)
			}
			if err := controller.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = controller.Wait()
			alive, err := LocalRunnerProcessAlive(localhostwire.LocalRunnerInstance{PID: report.PID, ProcessBirth: report.Birth})
			if err != nil || !alive {
				t.Fatalf("runner died with controller job: alive=%t err=%v", alive, err)
			}
			if _, err := fmt.Fprintln(runner, "survived"); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(runner).ReadString('\n')
			if err != nil || line != "survived\n" {
				t.Fatalf("runner response: %q %v", line, err)
			}
			state, err := windows.WaitForSingleObject(process, 10_000)
			if err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("runner did not exit: %d %v", state, err)
			}
			log := filepath.Join(home, "runners", LocalRunnerServiceName(home, "instance"), "runner.log")
			body, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(body), "runner stdout") || !strings.Contains(string(body), "runner stderr") {
				t.Fatalf("runner stdio: %q %v", body, err)
			}
		})
	}
}
