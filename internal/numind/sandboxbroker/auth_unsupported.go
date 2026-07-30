//go:build !linux

package sandboxbroker

func NewLinuxPeerAuthorizer([]uint32) (PeerAuthorizer, error) {
	return nil, ErrInvalidServerConfig
}
