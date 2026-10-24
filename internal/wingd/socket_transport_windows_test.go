package wingd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"golang.org/x/sys/windows"
)

func TestWindowsLocalTransportCarriesClientSID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := ListenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	owner, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			identity := connection.(authenticatedAPIConn).PeerIdentity()
			if identity.GOOS != "windows" || identity.SID == "" || identity.SID != owner.SID || identity.UID != nil {
				err = errors.New("API connection lost its authenticated client SID")
			}
			accepted <- connection
		}
		result <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialSocket(ctx, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
		defer server.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte("ready")); err != nil {
		t.Fatalf("listener close interrupted an authenticated connection: %v", err)
	}
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 5)
	if _, err := io.ReadFull(client, body); err != nil || string(body) != "ready" {
		t.Fatalf("application bytes changed across transport authentication: %q, %v", body, err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestWindowsLocalTransportListenerOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "d.sock")
	listener, err := bindSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if duplicate, err := bindSocket(path); err == nil {
		duplicate.Close()
		t.Fatal("second listener acquired a live pipe name")
	}
	result := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			connection.Close()
		}
		result <- err
	}()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := bindSocket(path)
	if err != nil {
		t.Fatalf("listener close retained its pipe instances: %v", err)
	}
	defer replacement.Close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("pending accept survived close: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("listener close did not join pending accept")
	}
}

func TestWindowsLocalTransportHasOwnerOnlyAccess(t *testing.T) {
	listener, err := bindSocket(filepath.Join(t.TempDir(), "api.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	pipe := listener.(*pipeListener)
	sd, err := windows.GetSecurityInfo(pipe.next, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := sd.Owner()
	if err != nil || actual == nil || actual.String() != owner.SID {
		t.Fatalf("pipe owner differs from controller: %v, %v", actual, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("pipe inherited access: %v, %v", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		t.Fatalf("pipe access is not limited to one principal: %v, %v", acl, err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() != owner.SID {
		t.Fatal("pipe grants another principal access")
	}
}

func TestWindowsListenerCloseCancelsPendingAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := bindSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	name, err := windows.UTF16PtrFromString(localPipePath(path))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := connectLocalPipe(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- checkPeerCredentials(connection) }()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("listener close retained an unauthenticated handle: %v", err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("client without identity reached local API")
		}
	case <-ctx.Done():
		t.Fatal("listener close did not end authentication")
	}
}

func TestWindowsLocalTransportCancelledHandshake(t *testing.T) {
	limit, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := bindSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		connection, err := DialSocket(ctx, path, 0)
		if connection != nil {
			connection.Close()
		}
		result <- err
	}()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	select {
	case connection := <-accepted:
		if connection == nil {
			t.Fatal("listener stopped before transport handshake")
		}
		defer connection.Close()
	case <-limit.Done():
		t.Fatal("dial did not reach the listening pipe")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled handshake returned %v", err)
		}
	case <-limit.Done():
		t.Fatal("cancelled handshake retained its connection")
	}
}

func FuzzWindowsLocalTransportIdentity(f *testing.F) {
	f.Add(byte(0), false, "S-1-5-18")
	f.Add(byte(0), true, "S-1-5-21-1000")
	f.Add(byte(1), false, "")
	f.Fuzz(func(t *testing.T, preamble byte, anonymous bool, forgedSID string) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		path := filepath.Join(t.TempDir(), "api.sock")
		listener, err := bindSocket(path)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		owner, err := controllerPeerIdentity()
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			connection, err := listener.Accept()
			if err == nil {
				defer connection.Close()
				var identity localhostwire.LocalPeerIdentity
				identity, err = acceptedPeerIdentity(connection)
				if err == nil && (identity.GOOS != "windows" || identity.SID != owner.SID || identity.UID != nil) {
					err = errors.New("request bytes replaced the kernel identity")
				}
			}
			result <- err
		}()
		name, err := windows.UTF16PtrFromString(localPipePath(path))
		if err != nil {
			t.Fatal(err)
		}
		flags := uint32(windows.FILE_FLAG_OVERLAPPED | windows.SECURITY_SQOS_PRESENT)
		if !anonymous {
			flags |= windows.SECURITY_IDENTIFICATION
		}
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, flags, 0)
		if err != nil {
			t.Fatal(err)
		}
		client := os.NewFile(uintptr(handle), "identity-fuzz")
		defer client.Close()
		if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if len(forgedSID) > 512 {
			forgedSID = forgedSID[:512]
		}
		if _, err := client.Write(append([]byte{preamble}, forgedSID...)); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if want := preamble == 0 && !anonymous; (err == nil) != want {
				t.Fatalf("preamble=%d, anonymous=%t: %v", preamble, anonymous, err)
			}
		case <-ctx.Done():
			t.Fatal("identity verification did not finish")
		}
	})
}
