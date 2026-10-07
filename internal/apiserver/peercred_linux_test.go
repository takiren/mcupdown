//go:build linux

package apiserver

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUID(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, ok := <-accepted
	if !ok {
		t.Fatal("accept failed")
	}
	defer func() { _ = server.Close() }()

	uid, ok := peerUID(server)
	if !ok || uid != uint32(os.Getuid()) {
		t.Errorf("peerUID = %d, %v; want %d, true", uid, ok, os.Getuid())
	}
}
