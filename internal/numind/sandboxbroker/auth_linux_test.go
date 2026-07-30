//go:build linux

package sandboxbroker

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthLinuxAllowsOnlyConfiguredPeerUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	client, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	allowed, err := NewLinuxPeerAuthorizer([]uint32{uint32(os.Getuid())})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := allowed.Authorize(server)
	if err != nil {
		t.Fatal(err)
	}
	if peer.UID != uint32(os.Getuid()) || peer.PID <= 0 {
		t.Fatalf("peer = %#v", peer)
	}

	deniedUID := uint32(os.Getuid()) + 1
	denied, err := NewLinuxPeerAuthorizer([]uint32{deniedUID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Authorize(server); err != ErrPeerUnauthorized {
		t.Fatalf("denied Authorize error = %v", err)
	}
}
