package sandboxbroker

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	// DefaultServerSocketPath is the only default broker transport endpoint.
	DefaultServerSocketPath = "/run/numind-sandbox/sandboxd.sock"
	// ServerSocketDirectoryMode is root:API-group with setgid inheritance.
	ServerSocketDirectoryMode uint32 = 0o2770
	// ServerSocketMode permits only broker owner and the dedicated API group.
	ServerSocketMode uint32 = 0o660
	// ServerMetadataMaxBytes bounds JSON bodies and HTTP headers.
	ServerMetadataMaxBytes int64 = 64 << 10
	// ServerMaxConnections bounds all accepted Unix connections.
	ServerMaxConnections = 32
	// ServerMaxCopyStreams bounds CopyIn and CopyOut together.
	ServerMaxCopyStreams = 4
	// ServerMaxLeaseDirectionStreams prevents overlapping writes or reads.
	ServerMaxLeaseDirectionStreams = 1
	// ServerCopyBytesPerSecond is the aggregate CopyIn+CopyOut rate.
	ServerCopyBytesPerSecond int64 = 100 << 20
	// ServerCopyBufferBytes is the only broker streaming buffer size.
	ServerCopyBufferBytes = 64 << 10
)

var (
	// ErrInvalidServerConfig means a Unix transport setting is unsafe.
	ErrInvalidServerConfig = errors.New("invalid sandbox broker server config")
	// ErrUnsafeServerSocket means the socket filesystem boundary is unsafe.
	ErrUnsafeServerSocket = errors.New("unsafe sandbox broker server socket")
)

// ServerConfig contains only Unix transport ownership and hard limits.
// There is deliberately no network/listen-address field.
type ServerConfig struct {
	SocketPath         string
	SocketDirectoryUID uint32
	SocketDirectoryGID uint32
	SocketUID          uint32
	SocketGID          uint32

	MetadataMaxBytes            int64
	MaxConnections              int
	MaxCopyStreams              int
	MaxLeaseDirectionStreams    int
	AggregateCopyBytesPerSecond int64
}

// DefaultServerConfig returns fixed ceilings with deployment-supplied IDs.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		SocketPath:                  DefaultServerSocketPath,
		MetadataMaxBytes:            ServerMetadataMaxBytes,
		MaxConnections:              ServerMaxConnections,
		MaxCopyStreams:              ServerMaxCopyStreams,
		MaxLeaseDirectionStreams:    ServerMaxLeaseDirectionStreams,
		AggregateCopyBytesPerSecond: ServerCopyBytesPerSecond,
	}
}

func (c ServerConfig) validate() error {
	if strings.ContainsRune(c.SocketPath, 0) ||
		!filepath.IsAbs(c.SocketPath) ||
		filepath.Clean(c.SocketPath) != c.SocketPath ||
		filepath.Base(c.SocketPath) == "." ||
		filepath.Base(c.SocketPath) == string(filepath.Separator) ||
		!strings.HasSuffix(filepath.Base(c.SocketPath), ".sock") ||
		len(c.SocketPath) > 100 {
		return ErrInvalidServerConfig
	}
	if c.MetadataMaxBytes <= 0 ||
		c.MetadataMaxBytes > ServerMetadataMaxBytes ||
		c.MaxConnections <= 0 ||
		c.MaxConnections > ServerMaxConnections ||
		c.MaxCopyStreams <= 0 ||
		c.MaxCopyStreams > ServerMaxCopyStreams ||
		c.MaxLeaseDirectionStreams != ServerMaxLeaseDirectionStreams ||
		c.AggregateCopyBytesPerSecond <= 0 ||
		c.AggregateCopyBytesPerSecond > ServerCopyBytesPerSecond {
		return ErrInvalidServerConfig
	}
	return nil
}
