//go:build !linux

// Platforms without Go's TCP splice path use adaptive user-space buffers.
package streamcopy

import "net"

// copyTCP specializes raw TCP pairs without bypassing other connection types.
func copyTCP(dst, src *net.TCPConn) (int64, error) {
	return copyBuffered(dst, src)
}
