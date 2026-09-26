//go:build !windows

package client

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func brokerAddress(dir string) string { return filepath.Join(dir, "broker.sock") }

func listenBroker(dir string) (brokerListener, error) {
	address := brokerAddress(dir)
	if info, err := os.Lstat(address); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("broker socket path is not a socket")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
			return nil, errors.New("broker socket has a different owner")
		}
		if err := os.Remove(address); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(address, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func dialBroker(ctx context.Context, dir string) (net.Conn, error) {
	if err := prepareCredentialDir(dir); err != nil {
		return nil, err
	}
	info, err := os.Lstat(brokerAddress(dir))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("broker socket requires user-only permissions")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", brokerAddress(dir))
	if err != nil {
		return nil, err
	}
	if err := verifyBrokerPeer(connection); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func detachBroker(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
