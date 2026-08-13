//go:build linux

package discord

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

func verifyPeer(conn net.Conn) error {
	sysConn, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("Discord IPC connection does not expose peer credentials")
	}
	rawConn, err := sysConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("get Discord IPC socket: %w", err)
	}
	var cred *syscall.Ucred
	var controlErr error
	if err := rawConn.Control(func(fd uintptr) {
		cred, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("inspect Discord IPC peer: %w", err)
	}
	if controlErr != nil {
		return fmt.Errorf("inspect Discord IPC peer: %w", controlErr)
	}
	if cred == nil || cred.Uid != uint32(os.Geteuid()) {
		if cred == nil {
			return fmt.Errorf("Discord IPC peer credentials unavailable")
		}
		return fmt.Errorf("Discord IPC peer UID %d does not match daemon UID %d", cred.Uid, os.Geteuid())
	}
	return nil
}
