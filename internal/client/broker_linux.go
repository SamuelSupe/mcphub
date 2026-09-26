package client

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func verifyBrokerPeer(connection net.Conn) error {
	raw, err := connection.(*net.UnixConn).SyscallConn()
	if err != nil {
		return err
	}
	var check error
	if err := raw.Control(func(fd uintptr) {
		credential, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			check = err
			return
		}
		if credential.Uid != uint32(os.Geteuid()) {
			check = errors.New("broker peer belongs to another user")
		}
	}); err != nil {
		return err
	}
	return check
}
