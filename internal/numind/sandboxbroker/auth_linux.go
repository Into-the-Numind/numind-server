//go:build linux

package sandboxbroker

import (
	"net"

	"golang.org/x/sys/unix"
)

type linuxPeerAuthorizer struct {
	allowed map[uint32]struct{}
}

// NewLinuxPeerAuthorizer permits only explicitly configured API host UIDs.
func NewLinuxPeerAuthorizer(allowedUIDs []uint32) (PeerAuthorizer, error) {
	if len(allowedUIDs) == 0 || len(allowedUIDs) > ServerMaxConnections {
		return nil, ErrInvalidServerConfig
	}
	authorizer := &linuxPeerAuthorizer{
		allowed: make(map[uint32]struct{}, len(allowedUIDs)),
	}
	for _, uid := range allowedUIDs {
		if _, duplicate := authorizer.allowed[uid]; duplicate {
			return nil, ErrInvalidServerConfig
		}
		authorizer.allowed[uid] = struct{}{}
	}
	return authorizer, nil
}

func (a *linuxPeerAuthorizer) Authorize(
	connection net.Conn,
) (PeerCredentials, error) {
	for {
		wrapper, ok := connection.(interface{ Unwrap() net.Conn })
		if !ok {
			break
		}
		connection = wrapper.Unwrap()
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	var credentials *unix.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = unix.GetsockoptUcred(
			int(fd),
			unix.SOL_SOCKET,
			unix.SO_PEERCRED,
		)
	}); err != nil {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	if controlErr != nil || credentials == nil {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	if _, allowed := a.allowed[credentials.Uid]; !allowed {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	if credentials.Pid <= 0 {
		return PeerCredentials{}, ErrPeerUnauthorized
	}
	return PeerCredentials{
		PID: credentials.Pid,
		UID: credentials.Uid,
		GID: credentials.Gid,
	}, nil
}
