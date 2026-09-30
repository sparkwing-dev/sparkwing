package wingd

import (
	"net"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestShutdownJoinsConnectionCleanupBeforePersistence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		d := configuredHandlerDaemon(t, Config{Runs: &FuncRunStore{IsTerminal: func(string) (bool, error) {
			close(entered)
			<-release
			return true, nil
		}}}, 2)
		persistedBeforeCleanup := false
		writes := 0
		d.persistWrite = func(string, admission.Snapshot, []admissionEvent, []string) error {
			d.mu.Lock()
			writes++
			persistedBeforeCleanup = len(d.conns) != 0
			d.mu.Unlock()
			return nil
		}
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()
		c, peer := newConn(d, server), newConn(d, client)
		d.startConn(c)
		if err := peer.send(&wingwire.Hello{ProtocolMajor: ProtocolMajor}); err != nil {
			t.Fatal(err)
		}
		if _, err := peer.readMessage(); err != nil {
			t.Fatal(err)
		}
		if err := peer.send(&wingwire.AdmissionRequest{RunID: "run", Resources: wingwire.HostResources{Cores: 1}}); err != nil {
			t.Fatal(err)
		}
		<-entered
		d.shutdown()
		stopped := make(chan struct{})
		go func() { d.finalShutdown(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Error("shutdown returned before the connection handler finished")
		default:
		}
		if persistedBeforeCleanup {
			t.Error("shutdown persisted before connection cleanup")
		}
		close(release)
		released = true
		<-stopped
		synctest.Wait()
		d.mu.Lock()
		remaining := len(d.conns)
		d.mu.Unlock()
		if remaining != 0 {
			t.Errorf("shutdown left %d connections", remaining)
		}
		if writes != 1 {
			t.Errorf("final persistence calls=%d, want one", writes)
		}
		if persistedBeforeCleanup {
			t.Error("final persistence preceded connection cleanup")
		}
	})
}
