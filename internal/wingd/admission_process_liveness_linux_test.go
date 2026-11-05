package wingd

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestAdmissionProcessBirthPreservesLiveProcessIdentity(t *testing.T) {
	want, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := admissionProcessBirth(os.Getpid()); err != nil || got != want {
		t.Fatalf("live process birth = %q, %v; want %q", got, err, want)
	}
}

func TestAdmissionUnreapedExitedHolderFreesCapacity(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	process := procenv.ControlCommand(nil, "/bin/cat")
	process.Stdin = read
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	birth, err := admissionProcessBirth(process.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	d := handlerDaemon(t, 1)
	request := AdmissionHTTPRequest{
		RequestKey: "native-holder", RootPID: process.Process.Pid, ProcessBirth: birth,
		Request: wingwire.AdmissionRequest{RunID: "holder", Resources: wingwire.HostResources{Cores: 1}},
	}
	response := policyHTTP(t, d, http.MethodPost, admissionHTTPPath+"/requests", request)
	var holder AdmissionHTTPState
	if err := json.Unmarshal(response.Body.Bytes(), &holder); err != nil || holder.Grant == nil {
		t.Fatalf("holder admission: HTTP %d: %s, %v", response.Code, response.Body.String(), err)
	}
	waiter := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "waiter", Resources: wingwire.HostResources{Cores: 1}})
	if waiter.Queued == nil {
		t.Fatalf("waiter = %+v, want queued", waiter)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, process.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if got, err := procgroup.ProcessBirth(process.Process.Pid); err != nil || got != birth {
		t.Fatalf("unreaped fixture birth = %q, %v; want %q", got, err, birth)
	}
	if _, err := admissionProcessBirth(process.Process.Pid); !errors.Is(err, procgroup.ErrProcessAbsent) {
		t.Fatalf("exited holder liveness = %v, want absent", err)
	}
	response = policyHTTP(t, d, http.MethodGet, admissionHTTPPath+"/requests/"+waiter.ID, nil)
	var promoted AdmissionHTTPState
	if err := json.Unmarshal(response.Body.Bytes(), &promoted); err != nil || promoted.Grant == nil {
		t.Fatalf("waiter after holder exit: HTTP %d: %s, %v", response.Code, response.Body.String(), err)
	}
}
