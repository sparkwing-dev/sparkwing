package wingd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func TestLocalAPIListenerRefusesUnverifiableConnection(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	var logs strings.Builder
	daemon := &Daemon{cfg: Config{Logf: func(format string, args ...any) { _, _ = fmt.Fprintf(&logs, format, args...) }}}
	listener := apiListener{Listener: &apiIdentityListener{connection: server}, d: daemon}
	connection, err := listener.Accept()
	if connection != nil || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("unverifiable connection yielded to API: %v, %v", connection, err)
	}
	if n, err := client.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("refused connection remained open: n=%d, err=%v", n, err)
	}
	if !strings.Contains(logs.String(), "peer identity unavailable") {
		t.Fatalf("missing identity was not reported: %s", logs.String())
	}
}

type apiIdentityListener struct{ connection net.Conn }

func (l *apiIdentityListener) Accept() (net.Conn, error) {
	if l.connection == nil {
		return nil, net.ErrClosed
	}
	connection := l.connection
	l.connection = nil
	return connection, nil
}

func (*apiIdentityListener) Close() error { return nil }
func (*apiIdentityListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "api-identity", Net: "unix"}
}

func TestLocalAPIConnectionCarriesVerifiedIdentity(t *testing.T) {
	uid := uint32(1000)
	for _, peer := range []localhostwire.LocalPeerIdentity{
		{GOOS: "linux", UID: &uid},
		{GOOS: "darwin", UID: &uid},
		{GOOS: "windows", SID: "S-1-5-21-1000"},
	} {
		t.Run(peer.GOOS, func(t *testing.T) {
			server, client := net.Pipe()
			defer func() { _ = server.Close() }()
			defer func() { _ = client.Close() }()
			connection, err := newAPIConn(server, peer, peer)
			if err != nil {
				t.Fatal(err)
			}
			var api APIConn = connection
			identity := api.PeerIdentity()
			if identity.GOOS != peer.GOOS || identity.SID != peer.SID || (peer.UID != nil && (identity.UID == nil || *identity.UID != *peer.UID)) {
				t.Fatalf("connection lost verified identity: %+v", identity)
			}
			if got, err := LocalSubject(identity, peer); err != nil || got == "" {
				t.Fatalf("controller cannot consume verified identity: %q, %v", got, err)
			}
		})
	}
}

func TestLocalAPIConnectionOwnsIdentitySnapshot(t *testing.T) {
	uid := uint32(1000)
	owner := localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid}
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	connection, err := newAPIConn(server, owner, owner)
	if err != nil {
		t.Fatal(err)
	}
	uid++
	first := connection.PeerIdentity()
	if first.UID == nil || *first.UID != 1000 {
		t.Fatal("transport input changed the connection's identity")
	}
	*first.UID = 0
	if second := connection.PeerIdentity(); second.UID == nil || *second.UID != 1000 {
		t.Fatal("API consumer changed the connection's identity")
	}
}

// safety: only a known matching transport identity can reach the local API.
func FuzzLocalAPIConnectionIdentity(f *testing.F) {
	f.Add(uint8(0), uint8(0), uint32(1000), uint32(1000), true, true, "", "")
	f.Add(uint8(1), uint8(1), uint32(0), uint32(1000), true, true, "", "")
	f.Add(uint8(2), uint8(2), uint32(0), uint32(0), false, false, "S-1-5-21-1000", "S-1-5-21-1000")
	f.Add(uint8(2), uint8(2), uint32(0), uint32(0), true, true, "", "")
	f.Add(uint8(3), uint8(3), uint32(0), uint32(0), true, true, "same", "same")
	f.Fuzz(func(t *testing.T, peerOS, ownerOS uint8, peerUID, ownerUID uint32, peerKnown, ownerKnown bool, peerSID, ownerSID string) {
		platforms := []string{"linux", "darwin", "windows", "unknown"}
		peer := localhostwire.LocalPeerIdentity{GOOS: platforms[int(peerOS)%len(platforms)], SID: peerSID}
		owner := localhostwire.LocalPeerIdentity{GOOS: platforms[int(ownerOS)%len(platforms)], SID: ownerSID}
		if peerKnown {
			peer.UID = &peerUID
		}
		if ownerKnown {
			owner.UID = &ownerUID
		}
		want := ""
		if peer.GOOS == owner.GOOS {
			switch peer.GOOS {
			case "linux", "darwin":
				if peerKnown && ownerKnown && peerUID == ownerUID {
					want = fmt.Sprintf("local:%d", peerUID)
				}
			case "windows":
				if peerSID != "" && peerSID == ownerSID {
					want = "local:" + peerSID
				}
			}
		}
		server, client := net.Pipe()
		defer func() { _ = server.Close() }()
		defer func() { _ = client.Close() }()
		connection, err := newAPIConn(server, peer, owner)
		if want == "" {
			if err == nil || connection != nil {
				t.Fatal("unverified or foreign identity reached local API")
			}
			return
		}
		if err != nil || connection == nil {
			t.Fatalf("matching identity refused: %v", err)
		}
		identity := connection.PeerIdentity()
		if got, err := LocalSubject(identity, owner); err != nil || got != want {
			t.Fatalf("verified identity changed: %q, %v", got, err)
		}
		if identity.UID != nil {
			*identity.UID = ^*identity.UID
		}
		if got, err := LocalSubject(connection.PeerIdentity(), owner); err != nil || got != want {
			t.Fatal("consumer changed verified connection identity")
		}
	})
}
