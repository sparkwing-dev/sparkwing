package chaos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const actorHelperMode = "SPARKWING_CHAOS_ACTOR_HELPER"

func TestWatchActorHelperProcess(t *testing.T) {
	switch os.Getenv(actorHelperMode) {
	case "descendant":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "actor":
		child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		child.Env = append(os.Environ(), actorHelperMode+"=descendant")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("SPARKWING_CHAOS_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(3)
		}
		fmt.Println("OK sentinel-immediately-before-exit")
		os.Exit(0)
	}
}

func TestWatchActorReapsExitedProcessAndRecordsFinalOutput(t *testing.T) {
	root := t.TempDir()
	journal, err := NewJournal(filepath.Join(root, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	childPIDPath := filepath.Join(root, "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
	cmd.Env = append(os.Environ(), actorHelperMode+"=actor", "SPARKWING_CHAOS_CHILD_PID="+childPIDPath)
	stdout, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		body, err := os.ReadFile(childPIDPath)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
		if err != nil {
			return
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Kill()
			_, _ = process.Wait()
		}
	})

	h := &Harness{cfg: Config{Settle: time.Second}, jr: journal}
	a := &actor{runID: "exited-parent", cmd: cmd}
	go h.watchActor(a, stdout)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		exited, granted := a.exited, a.granted
		h.mu.Unlock()
		if exited {
			if !granted {
				t.Fatal("final protocol output was lost before the actor was reaped")
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("actor was marked exited before its process was reaped")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("exited actor was not reaped while its descendant held stdout open")
}
