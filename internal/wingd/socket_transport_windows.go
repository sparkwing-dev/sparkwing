package wingd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"golang.org/x/sys/windows"
)

type pipeAddress string

func (pipeAddress) Network() string  { return "pipe" }
func (p pipeAddress) String() string { return string(p) }

type pipeConn struct {
	*os.File
	path     string
	server   bool
	identity localhostwire.LocalPeerIdentity
	peerOnce sync.Once
	peerErr  error
	stopAuth func() bool
	listener *pipeListener
}

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddress(c.path) }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddress(c.path) }

func (c *pipeConn) Close() error {
	err := c.File.Close()
	if c.stopAuth != nil {
		c.stopAuth()
	}
	return err
}

type pipeListener struct {
	mu       sync.Mutex
	path     string
	owner    *windows.SECURITY_DESCRIPTOR
	next     windows.Handle
	closed   bool
	pending  map[windows.Handle]*windows.Overlapped
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
	done     chan struct{}
	closeErr error
}

func localPipePath(path string) string {
	key := sha256.Sum256([]byte(strings.ToUpper(filepath.Clean(path))))
	return fmt.Sprintf(`\\.\pipe\sparkwing-%x`, key)
}

func bindSocket(path string) (net.Listener, error) {
	if err := ensureSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	owner, err := controllerPeerIdentity()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + owner.SID + "D:P(A;;GA;;;" + owner.SID + ")")
	if err != nil {
		return nil, err
	}
	l := &pipeListener{path: localPipePath(path), owner: sd, pending: make(map[windows.Handle]*windows.Overlapped), done: make(chan struct{})}
	l.next, err = l.instance(windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	if err != nil {
		return nil, fmt.Errorf("wingd: listen %s: %w", path, err)
	}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	return l, nil
}

func (l *pipeListener) instance(extra uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(l.path)
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: l.owner}
	sa.Length = uint32(unsafe.Sizeof(sa))
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|extra,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, &sa)
}

func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.accept()
		if err != windows.ERROR_NO_DATA && err != windows.ERROR_PIPE_NOT_CONNECTED {
			return connection, err
		}
	}
}

func (l *pipeListener) accept() (net.Conn, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(event)
	overlap := &windows.Overlapped{HEvent: event}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	l.wait.Add(1)
	defer l.wait.Done()
	handle := l.next
	l.next = 0
	if handle == 0 {
		handle, err = l.instance(0)
	}
	if err == nil {
		l.pending[handle] = overlap
		err = windows.ConnectNamedPipe(handle, overlap)
	}
	l.mu.Unlock()
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var n uint32
		err = windows.GetOverlappedResult(handle, overlap, &n, true)
	} else if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		err = nil
	}
	l.mu.Lock()
	delete(l.pending, handle)
	closed := l.closed
	if !closed && l.next == 0 {
		if next, nextErr := l.instance(0); nextErr != nil {
			err = errors.Join(err, nextErr)
		} else {
			l.next = next
		}
	}
	var connection *pipeConn
	if err == nil && !closed {
		connection = &pipeConn{File: os.NewFile(uintptr(handle), l.path), path: l.path, server: true, listener: l}
		l.wait.Add(1)
		stop := context.AfterFunc(l.ctx, func() {
			connection.File.Close()
			l.wait.Done()
		})
		connection.stopAuth = func() bool {
			if stop() {
				l.wait.Done()
				return true
			}
			return false
		}
	}
	l.mu.Unlock()
	if err != nil || closed {
		if handle != 0 && handle != windows.InvalidHandle {
			windows.CloseHandle(handle)
		}
		if closed {
			return nil, net.ErrClosed
		}
		return nil, err
	}
	return connection, nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddress(l.path) }

func (l *pipeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return l.closeErr
	}
	l.closed = true
	l.cancel()
	var err error
	if l.next != 0 {
		err = windows.CloseHandle(l.next)
		l.next = 0
	}
	for handle, overlap := range l.pending {
		if cancelErr := windows.CancelIoEx(handle, overlap); cancelErr != nil && !errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
			err = errors.Join(err, cancelErr)
		}
	}
	l.mu.Unlock()
	l.wait.Wait()
	l.closeErr = err
	close(l.done)
	return err
}

func (l *pipeListener) startAuthentication() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.wait.Add(1)
	return true
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(localPipePath(path))
	if err != nil {
		return nil, err
	}
	handle, err := connectLocalPipe(ctx, name)
	if err != nil {
		return nil, &os.PathError{Op: "connect", Path: path, Err: err}
	}
	connection := &pipeConn{File: os.NewFile(uintptr(handle), path), path: localPipePath(path)}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err := connection.verifyServer(handle); err != nil {
		connection.Close()
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		err = connection.SetDeadline(deadline)
	}
	if err == nil {
		err = pipePreamble(connection)
	}
	if err == nil {
		err = connection.SetDeadline(time.Time{})
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func connectLocalPipe(ctx context.Context, name *uint16) (windows.Handle, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return handle, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *pipeConn) verifyServer(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	self, err := controllerPeerIdentity()
	if err != nil {
		return err
	}
	if owner == nil || owner.String() != self.SID {
		return errors.New("wingd: local pipe belongs to another account")
	}
	return nil
}

func pipePreamble(c net.Conn) error {
	if _, err := c.Write([]byte{0}); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		return err
	}
	if ack[0] != 0 {
		return errors.New("wingd: invalid local pipe authentication reply")
	}
	return nil
}

func socketPresent(string) bool { return true }
