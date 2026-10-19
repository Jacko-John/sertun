// Buffered TCP connections preserve bytes read ahead during a handshake while
// allowing Copy to recover the native connection once those bytes are drained.

package streamcopy

import (
	"bufio"
	"io"
	"net"
	"sync"
)

// WithReadBuffer returns conn with reader's pending bytes prepended to future
// reads. The reader must contain unmodified bytes read from conn. Its underlying
// reader is never used again, so a handshake limit cannot truncate the stream.
// The caller transfers exclusive ownership of reader to the returned connection.
func WithReadBuffer(conn *net.TCPConn, reader *bufio.Reader) net.Conn {
	if reader.Buffered() == 0 {
		return conn
	}
	return &bufferedConn{TCPConn: conn, reader: reader}
}

// bufferedConn must stay a net.Conn and keep both copy specializations so
// io.Copy cannot bypass the buffered prefix.
var (
	_ net.Conn      = (*bufferedConn)(nil)
	_ io.WriterTo   = (*bufferedConn)(nil)
	_ io.ReaderFrom = (*bufferedConn)(nil)
)

// bufferedConn serves the handshake read-ahead before the socket itself. It
// embeds *net.TCPConn and overrides Read, WriteTo, and ReadFrom.
type bufferedConn struct {
	*net.TCPConn
	readMu sync.Mutex
	reader *bufio.Reader
}

// Read drains pending handshake bytes before reading directly from TCP.
func (c *bufferedConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.reader != nil {
		n, err := c.reader.Read(p)
		if c.reader.Buffered() == 0 {
			c.reader = nil
		}
		return n, err
	}
	return c.TCPConn.Read(p)
}

// WriteTo overrides the embedded TCP method so even io.Copy preserves the
// buffered prefix before recovering the platform's TCP transfer policy.
func (c *bufferedConn) WriteTo(dst io.Writer) (int64, error) {
	return Copy(dst, c)
}

// ReadFrom applies the same policy in the write direction without touching the
// pending read buffer, allowing simultaneous reads and writes.
func (c *bufferedConn) ReadFrom(src io.Reader) (int64, error) {
	return Copy(c.TCPConn, src)
}

// writeBuffered forwards only bytes already read during the handshake. A short
// or failed write consumes just the bytes delivered and leaves the rest readable.
func (c *bufferedConn) writeBuffered(dst io.Writer) (int64, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.reader == nil {
		return 0, nil
	}
	// size bytes are already buffered, so Peek and Discard cannot fail.
	size := c.reader.Buffered()
	data, _ := c.reader.Peek(size)
	written, err := dst.Write(data)
	if written < 0 || written > size {
		return 0, errInvalidWrite
	}
	_, _ = c.reader.Discard(written)
	if c.reader.Buffered() == 0 {
		c.reader = nil
	}
	if written != size && err == nil {
		err = io.ErrShortWrite
	}
	return int64(written), err
}
