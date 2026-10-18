// Platforms with native TCP copy support keep the standard library's fast path.
//go:build !darwin

package relay

import (
	"io"
	"net"
)

// copyPayload lets io.Copy select WriterTo/ReaderFrom, including Linux splice.
func copyPayload(dst, src net.Conn) (int64, error) {
	return io.Copy(dst, src)
}
