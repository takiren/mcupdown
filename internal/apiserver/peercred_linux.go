//go:build linux

package apiserver

import (
	"net"
	"syscall"
)

const peerCredSupported = true

// peerUID は Unix ソケットの接続元の UID を SO_PEERCRED で取得する。
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return 0, false
	}
	return cred.Uid, true
}
