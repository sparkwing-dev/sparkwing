package wingd_test

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func dialHandshaked(t *testing.T, home string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	return dialAs(t, home, false)
}

func dialAs(t *testing.T, home string, probe bool) (net.Conn, *bufio.Scanner) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	if err := writeRawMessage(nc, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test", HealthProbe: probe}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	sc := bufio.NewScanner(nc)
	if _, ok := readFrame(t, nc, sc).(*wingwire.HelloAck); !ok {
		t.Fatal("daemon did not answer the handshake with hello_ack")
	}
	return nc, sc
}

func readFrame(t *testing.T, nc net.Conn, sc *bufio.Scanner) wingwire.Message {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if !sc.Scan() {
		t.Fatalf("read frame: %v", sc.Err())
	}
	msg, err := wingwire.Decode(sc.Bytes())
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return msg
}

func TestUnknownMessageTypeIsAnsweredAndTheConnectionSurvives(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialHandshaked(t, home)
	if _, err := nc.Write([]byte(`{"type":"warp_core_breach","payload":{}}` + "\n")); err != nil {
		t.Fatalf("write unknown frame: %v", err)
	}
	msg := readFrame(t, nc, sc)
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		t.Fatalf("reply = %T, want *wingwire.Unsupported", msg)
	}
	if unsupported.Type != "warp_core_breach" {
		t.Errorf("unsupported names %q, want warp_core_breach", unsupported.Type)
	}

	if err := writeRawMessage(nc, &wingwire.QueueState{}); err != nil {
		t.Fatalf("write queue state on the surviving connection: %v", err)
	}
	if _, ok := readFrame(t, nc, sc).(*wingwire.QueueState); !ok {
		t.Error("the connection did not keep serving after an unknown frame")
	}
}

func TestKnownButUnservedMessageTypeIsAnswered(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialHandshaked(t, home)
	if err := writeRawMessage(nc, &wingwire.Grant{RunID: "r1"}); err != nil {
		t.Fatalf("write grant: %v", err)
	}
	msg := readFrame(t, nc, sc)
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		t.Fatalf("reply = %T, want *wingwire.Unsupported", msg)
	}
	if unsupported.Type != string(wingwire.TypeGrant) {
		t.Errorf("unsupported names %q, want %q", unsupported.Type, wingwire.TypeGrant)
	}
}

func dialRaw(t *testing.T, home string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return nc, bufio.NewScanner(nc)
}

func writeUnknownFrame(t *testing.T, nc net.Conn, name string) {
	t.Helper()
	if _, err := nc.Write([]byte(`{"type":"` + name + `","payload":{}}` + "\n")); err != nil {
		t.Fatalf("write unknown frame: %v", err)
	}
}

func expectClosed(t *testing.T, nc net.Conn, sc *bufio.Scanner) {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if sc.Scan() {
		t.Fatalf("connection still open, read %s", sc.Bytes())
	}
}

func TestUnsupportedRepliesAreBoundedPerConnection(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialHandshaked(t, home)
	for i := range wingd.MaxUnsupportedReplies {
		writeUnknownFrame(t, nc, "warp_core_breach")
		if _, ok := readFrame(t, nc, sc).(*wingwire.Unsupported); !ok {
			t.Fatalf("refusal %d was not an unsupported reply", i+1)
		}
	}
	expectClosed(t, nc, sc)
}

func TestRefusalTruncatesThePeersTypeName(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialHandshaked(t, home)
	writeUnknownFrame(t, nc, strings.Repeat("z", 4096))
	msg := readFrame(t, nc, sc)
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		t.Fatalf("reply = %T, want *wingwire.Unsupported", msg)
	}
	if len(unsupported.Type) != wingd.MaxRefusedTypeName {
		t.Errorf("echoed type is %d bytes, want it capped at %d", len(unsupported.Type), wingd.MaxRefusedTypeName)
	}
}

func TestMalformedFrameStillEndsTheConnection(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	for _, frame := range []string{"{}", `{"payload":{}}`, "not json"} {
		nc, sc := dialHandshaked(t, home)
		if _, err := nc.Write([]byte(frame + "\n")); err != nil {
			t.Fatalf("write %s: %v", frame, err)
		}
		expectClosed(t, nc, sc)
	}
}

func TestHealthProbeConnectionIsAnsweredTheSameWay(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialAs(t, home, true)
	writeUnknownFrame(t, nc, "warp_core_breach")
	if _, ok := readFrame(t, nc, sc).(*wingwire.Unsupported); !ok {
		t.Error("a probe connection was not told the unknown type is unserved")
	}
	if err := writeRawMessage(nc, &wingwire.Grant{RunID: "r1"}); err != nil {
		t.Fatalf("write grant: %v", err)
	}
	if _, ok := readFrame(t, nc, sc).(*wingwire.Unsupported); !ok {
		t.Error("a probe connection was not told a known-unserved type is unserved")
	}
}

func TestUnknownFrameBeforeTheHandshakeIsAnsweredThenClosed(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	nc, sc := dialRaw(t, home)
	writeUnknownFrame(t, nc, "warp_core_breach")
	if _, ok := readFrame(t, nc, sc).(*wingwire.Unsupported); !ok {
		t.Fatal("an unknown first frame was not answered")
	}
	expectClosed(t, nc, sc)

	nc2, sc2 := dialRaw(t, home)
	if err := writeRawMessage(nc2, &wingwire.Grant{RunID: "r1"}); err != nil {
		t.Fatalf("write grant: %v", err)
	}
	if _, ok := readFrame(t, nc2, sc2).(*wingwire.Unsupported); !ok {
		t.Fatal("a non-hello first frame was not answered")
	}
	expectClosed(t, nc2, sc2)
}

func TestUnknownFramesDoNotHoldTheDaemonPastIdle(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})

	nc, _ := dialHandshaked(t, home)
	go func() {
		for {
			if _, err := nc.Write([]byte(`{"type":"warp_core_breach","payload":{}}` + "\n")); err != nil {
				return
			}
		}
	}()
	if err := td.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("daemon exited with %v", err)
	}
}
