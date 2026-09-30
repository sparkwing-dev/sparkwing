package wingd

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestHeartbeatCounterAdvancesAndStopsWithShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, err := New(Config{Home: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.layout.ensureDir(); err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		go d.heartbeatLoop(stop, done)
		synctest.Wait()
		path := filepath.Join(d.layout.dir, "heartbeat")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != heartbeatSize {
			t.Fatalf("heartbeat size = %d, want %d", info.Size(), heartbeatSize)
		}
		first, err := ReadHeartbeat(path)
		if err != nil || first == 0 {
			t.Fatalf("first heartbeat = %d, %v", first, err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		second, err := ReadHeartbeat(path)
		if err != nil || second <= first {
			t.Fatalf("next heartbeat = %d, %v; first = %d", second, err, first)
		}
		d.shutdown()
		<-done
		stopped, err := ReadHeartbeat(path)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if got, err := ReadHeartbeat(path); err != nil || got != stopped {
			t.Fatalf("heartbeat after shutdown = %d, %v; want %d", got, err, stopped)
		}
		close(stop)
	})
}

func TestHeartbeatWaitsForAdmissionLock(t *testing.T) {
	d, err := New(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.layout.ensureDir(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.layout.dir, "heartbeat")
	writer, err := openHeartbeat(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	d.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := d.heartbeatTick(writer, 1)
		done <- err
	}()
	<-started
	if got, err := ReadHeartbeat(path); err != nil || got != 0 {
		d.mu.Unlock()
		t.Fatalf("heartbeat while locked = %d, %v; want zero", got, err)
	}
	d.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, err := ReadHeartbeat(path); err != nil || got != 1 {
		t.Fatalf("heartbeat after unlock = %d, %v; want one", got, err)
	}
}
