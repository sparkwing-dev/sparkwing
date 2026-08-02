package chaos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const actorHelperMode = "SPARKWING_CHAOS_ACTOR_HELPER"

func TestWatchActorHelperProcess(t *testing.T) {
	switch os.Getenv(actorHelperMode) {
	case "descendant":
		ignoreProcessGroupTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "actor":
		children, err := strconv.Atoi(os.Getenv("SPARKWING_CHAOS_CHILDREN"))
		if err != nil || children < 1 {
			children = 1
		}
		for range children {
			child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
			child.Env = append(os.Environ(), actorHelperMode+"=descendant")
			child.Stdout = os.Stdout
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
		}
		time.Sleep(250 * time.Millisecond)
		fmt.Println("OK sentinel-immediately-before-exit")
		os.Exit(0)
	case "daemon":
		child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		child.Env = append(os.Environ(), actorHelperMode+"=descendant")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(250 * time.Millisecond)
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

	cmd := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
	cmd.Env = append(os.Environ(), actorHelperMode+"=actor")
	stdout, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}

	h := &Harness{cfg: Config{Settle: time.Second}, t: t, jr: journal}
	a := &actor{runID: "exited-parent", cmd: cmd, pgid: cmd.Process.Pid}
	t.Cleanup(func() { _ = killProcessGroup(a.pgid) })
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
			alive, aliveErr := processGroupAlive(a.pgid)
			if aliveErr != nil {
				t.Fatal(aliveErr)
			}
			if alive {
				t.Fatal("actor process group remained alive after an ignore-term descendant")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("exited actor was not reaped while its descendant held stdout open")
}

func TestWatchActorBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	root := t.TempDir()
	journal, err := NewJournal(filepath.Join(root, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	h := &Harness{cfg: Config{Settle: time.Second}, t: t, jr: journal}
	const actorCount = 20
	var wg sync.WaitGroup
	groups := make([]int, 0, actorCount)
	for i := range actorCount {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		cmd.Env = append(os.Environ(), actorHelperMode+"=actor", "SPARKWING_CHAOS_CHILDREN=3")
		stdout, startErr := startActorCommand(cmd)
		if startErr != nil {
			t.Fatal(startErr)
		}
		a := &actor{runID: fmt.Sprintf("churn-%d", i), cmd: cmd, pgid: cmd.Process.Pid}
		groups = append(groups, a.pgid)
		t.Cleanup(func() { _ = killProcessGroup(a.pgid) })
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.watchActor(a, stdout)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("repeated actor cleanup exceeded its bound")
	}
	for _, pgid := range groups {
		alive, aliveErr := processGroupAlive(pgid)
		if aliveErr != nil {
			t.Fatal(aliveErr)
		}
		if alive {
			t.Fatalf("process group %d remained alive after churn", pgid)
		}
	}
}

func TestManagedDaemonBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	h := &Harness{cfg: Config{Settle: time.Second}, t: t, daemons: map[int]*daemonProcess{}}
	const daemonCount = 20
	groups := make([]int, 0, daemonCount)
	for range daemonCount {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		cmd.Env = append(os.Environ(), actorHelperMode+"=daemon")
		if err := h.startDaemonCommand(cmd); err != nil {
			t.Fatal(err)
		}
		groups = append(groups, cmd.Process.Pid)
		t.Cleanup(func() { _ = killProcessGroup(cmd.Process.Pid) })
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		remaining := len(h.daemons)
		h.mu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	remaining := len(h.daemons)
	h.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d managed daemon groups remained after cleanup bound", remaining)
	}
	for _, pgid := range groups {
		alive, aliveErr := processGroupAlive(pgid)
		if aliveErr != nil {
			t.Fatal(aliveErr)
		}
		if alive {
			t.Fatalf("daemon process group %d remained alive after churn", pgid)
		}
	}
}
