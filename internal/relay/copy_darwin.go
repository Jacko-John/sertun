// macOS has no TCP splice path in Go. Larger reusable copy buffers reduce
// read/write syscall frequency without changing stream or half-close semantics.
package relay

import (
	"io"
	"net"
	"sync"
)

const promoteAfter = 1 << 20

var smallTCPBuffers = sync.Pool{New: func() any {
	buffer := make([]byte, 32<<10)
	return &buffer
}}

var largeTCPBuffers = sync.Pool{New: func() any {
	buffer := make([]byte, 256<<10)
	return &buffer
}}

type readOnly struct{ io.Reader }
type writeOnly struct{ io.Writer }

// copyPayload specializes only raw TCP pairs; other connection implementations
// retain their own copy methods. Hiding TCP copy methods makes the supplied
// buffer effective instead of silently falling back to Go's 32 KiB buffer.
func copyPayload(dst, src net.Conn) (int64, error) {
	if _, ok := src.(*net.TCPConn); !ok {
		return io.Copy(dst, src)
	}
	if _, ok := dst.(*net.TCPConn); !ok {
		return io.Copy(dst, src)
	}
	// Short streams retain the original per-direction buffer budget. Upgrade
	// only after sustained payload, returning the small buffer before growing.
	buffer := smallTCPBuffers.Get().(*[]byte)
	initial := &io.LimitedReader{R: src, N: promoteAfter}
	written, err := io.CopyBuffer(writeOnly{dst}, initial, *buffer)
	smallTCPBuffers.Put(buffer)
	if err != nil || initial.N != 0 {
		return written, err
	}
	buffer = largeTCPBuffers.Get().(*[]byte)
	defer largeTCPBuffers.Put(buffer)
	continued, err := io.CopyBuffer(writeOnly{dst}, readOnly{src}, *buffer)
	return written + continued, err
}
