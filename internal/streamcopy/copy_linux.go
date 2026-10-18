// Linux TCP transfers retain Go's pooled splice implementation and netpoll.
package streamcopy

import "net"

// copyTCP lets Go attempt splice and safely choose its own fallback when the
// native transfer is unsupported. Errors after progress are returned as-is.
func copyTCP(dst, src *net.TCPConn) (int64, error) {
	return dst.ReadFrom(src)
}
