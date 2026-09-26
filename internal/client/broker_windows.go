package client

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/sys/windows"
)

type pipeAddress string

func (a pipeAddress) Network() string { return "pipe" }
func (a pipeAddress) String() string  { return string(a) }

type pipeConnection struct {
	handle                      windows.Handle
	server                      bool
	closed                      atomic.Bool
	readDeadline, writeDeadline atomic.Int64
	readMu, writeMu             sync.Mutex
}

func (p *pipeConnection) LocalAddr() net.Addr  { return pipeAddress("mcphub") }
func (p *pipeConnection) RemoteAddr() net.Addr { return pipeAddress("mcphub") }
func deadlineValue(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
func (p *pipeConnection) SetReadDeadline(t time.Time) error {
	p.readDeadline.Store(deadlineValue(t))
	return nil
}
func (p *pipeConnection) SetWriteDeadline(t time.Time) error {
	p.writeDeadline.Store(deadlineValue(t))
	return nil
}
func (p *pipeConnection) SetDeadline(t time.Time) error {
	p.SetReadDeadline(t)
	return p.SetWriteDeadline(t)
}
func (p *pipeConnection) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = windows.CancelIoEx(p.handle, nil)
	return windows.CloseHandle(p.handle)
}

func (p *pipeConnection) Read(data []byte) (int, error) {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	return p.transfer(data, false)
}
func (p *pipeConnection) Write(data []byte) (int, error) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.transfer(data, true)
}
func (p *pipeConnection) transfer(data []byte, write bool) (int, error) {
	if p.closed.Load() {
		return 0, net.ErrClosed
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	var count uint32
	if write {
		err = windows.WriteFile(p.handle, data, &count, &overlap)
	} else {
		err = windows.ReadFile(p.handle, data, &count, &overlap)
	}
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return 0, io.EOF
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		for {
			deadline := p.readDeadline.Load()
			if write {
				deadline = p.writeDeadline.Load()
			}
			if p.closed.Load() || (deadline != 0 && time.Now().UnixNano() >= deadline) {
				_ = windows.CancelIoEx(p.handle, &overlap)
				_ = windows.GetOverlappedResult(p.handle, &overlap, &count, true)
				if p.closed.Load() {
					return int(count), net.ErrClosed
				}
				return int(count), os.ErrDeadlineExceeded
			}
			state, waitErr := windows.WaitForSingleObject(event, 100)
			if waitErr != nil {
				_ = windows.CancelIoEx(p.handle, &overlap)
				_ = windows.GetOverlappedResult(p.handle, &overlap, &count, true)
				return 0, waitErr
			}
			if state == uint32(windows.WAIT_TIMEOUT) {
				continue
			}
			err = windows.GetOverlappedResult(p.handle, &overlap, &count, true)
			break
		}
	}
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		err = io.EOF
	}
	return int(count), err
}

type pipeListener struct {
	name    string
	mu      sync.Mutex
	closed  bool
	pending windows.Handle
	first   bool
}

func brokerPipeName(dir string) (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return `\\.\pipe\mcphub-` + configstore.SecretHash(user.User.Sid.String() + "|" + abs)[:32], nil
}

func listenBroker(dir string) (brokerListener, error) {
	name, err := brokerPipeName(dir)
	if err != nil {
		return nil, err
	}
	return &pipeListener{name: name, first: true}, nil
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	name, _ := windows.UTF16PtrFromString(l.name)
	sa, err := credentialSecurity()
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if l.first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	handle, err := windows.CreateNamedPipe(name, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES, 65536, 65536, 0, sa)
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	l.first, l.pending = false, handle
	l.mu.Unlock()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(handle, &overlap)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var count uint32
		err = windows.GetOverlappedResult(handle, &overlap, &count, true)
	}
	l.mu.Lock()
	closed := l.closed
	l.pending = 0
	l.mu.Unlock()
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		if !closed {
			windows.CloseHandle(handle)
		}
		return nil, err
	}
	if closed {
		return nil, net.ErrClosed
	}
	return &pipeConnection{handle: handle, server: true}, nil
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.pending != 0 {
		_ = windows.CancelIoEx(l.pending, nil)
		_ = windows.CloseHandle(l.pending)
	}
	return nil
}

func dialBroker(ctx context.Context, dir string) (net.Conn, error) {
	if err := prepareCredentialDir(dir); err != nil {
		return nil, err
	}
	address, err := brokerPipeName(dir)
	if err != nil {
		return nil, err
	}
	name, _ := windows.UTF16PtrFromString(address)
	for {
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			connection := &pipeConnection{handle: handle}
			if err := verifyBrokerPeer(connection); err != nil {
				connection.Close()
				return nil, err
			}
			return connection, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func verifyBrokerPeer(connection net.Conn) error {
	p := connection.(*pipeConnection)
	var pid uint32
	var err error
	if p.server {
		err = windows.GetNamedPipeClientProcessId(p.handle, &pid)
	} else {
		err = windows.GetNamedPipeServerProcessId(p.handle, &pid)
	}
	if err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	peer, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !peer.User.Sid.Equals(user.User.Sid) {
		return errors.New("broker peer belongs to another Windows user")
	}
	return nil
}

func detachBroker(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
