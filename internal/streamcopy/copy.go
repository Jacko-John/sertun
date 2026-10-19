// Package streamcopy selects native TCP transfers or reusable user-space
// buffers. Callers retain connection ownership, cancellation and half-close.
package streamcopy

import (
	"errors"
	"io"
	"net"
	"sync"
)

const (
	smallBufferSize = 32 << 10
	largeBufferSize = 256 << 10
	promoteAfter    = 1 << 20
)

// Pool entries are shared across connections; the GC may reclaim idle ones.
var smallBuffers = sync.Pool{New: func() any { return new([smallBufferSize]byte) }}
var largeBuffers = sync.Pool{New: func() any { return new([largeBufferSize]byte) }}
var errInvalidWrite = errors.New("streamcopy: invalid write count")

// Copy transfers bytes until EOF or the first error and returns the number
// written. It does not close either endpoint. Only this package's read-buffer
// wrapper is unwrapped; other types keep their own I/O and copy semantics.
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	// Writes never use a connection's pending read buffer. Reads must forward
	// that buffer before selecting a native path, without reading more into it.
	if conn, ok := dst.(*bufferedConn); ok {
		dst = conn.TCPConn
	}
	var buffered int64
	if conn, ok := src.(*bufferedConn); ok {
		var err error
		buffered, err = conn.writeBuffered(dst)
		if err != nil {
			return buffered, err
		}
		src = conn.TCPConn
	}

	written, err := copyStream(dst, src)
	return buffered + written, err
}

// copyStream selects a platform TCP policy before preserving specialized copy
// methods on other types, including wrappers that transform or count data.
func copyStream(dst io.Writer, src io.Reader) (int64, error) {
	if source, ok := src.(*net.TCPConn); ok {
		if destination, ok := dst.(*net.TCPConn); ok {
			return copyTCP(destination, source)
		}
	}
	if source, ok := src.(io.WriterTo); ok {
		return source.WriteTo(dst)
	}
	if destination, ok := dst.(io.ReaderFrom); ok {
		return destination.ReadFrom(src)
	}
	return copyBuffered(dst, src)
}

// copyBuffered keeps short streams small and promotes sustained transfers.
// The direct read/write loop avoids per-call LimitedReader and interface
// wrappers. Writes reuse the read buffer without another payload copy or wait
// for that buffer to fill.
func copyBuffered(dst io.Writer, src io.Reader) (int64, error) {
	small := smallBuffers.Get().(*[smallBufferSize]byte)
	var large *[largeBufferSize]byte
	defer func() {
		if small != nil {
			smallBuffers.Put(small)
		}
		if large != nil {
			largeBuffers.Put(large)
		}
	}()

	buffer := small[:]
	var written int64
	for {
		// Deliver bytes returned alongside EOF or a read error before
		// reporting it. A partial write ends the copy with exact progress.
		read, readErr := src.Read(buffer)
		if read > 0 {
			n, err := dst.Write(buffer[:read])
			if n < 0 || n > read {
				return written, errInvalidWrite
			}
			written += int64(n)
			if err != nil {
				return written, err
			}
			if n != read {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				readErr = nil
			}
			return written, readErr
		}

		// Return the small buffer before borrowing a large one. Idle pool
		// entries may be reclaimed by GC; connections retain neither buffer.
		if large == nil && written >= promoteAfter {
			smallBuffers.Put(small)
			small = nil
			large = largeBuffers.Get().(*[largeBufferSize]byte)
			buffer = large[:]
		}
	}
}
