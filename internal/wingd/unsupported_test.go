package wingd_test

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func dialHandshaked(t *testing.T, home string) (net.Conn, *bufio.Scanner) {
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
	if err := writeRawMessage(nc, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test"}); err != nil {
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
